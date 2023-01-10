/*
Copyright 2023 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package upsync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	controllerName = "kcp-resource-upsyncer"
	labelKey       = "kcp.resource.upsync"
	syncValue      = "sync"
)

type Controller struct {
	queue            workqueue.RateLimitingInterface
	upstreamClient   kcpdynamic.ClusterInterface
	downstreamClient dynamic.Interface

	getDownstreamLister       func(gvr schema.GroupVersionResource) (cache.GenericLister, error)
	getUpstreamUpsyncerLister func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error)

	syncTargetName      string
	syncTargetWorkspace logicalcluster.Name
	syncTargetUID       types.UID
	syncTargetKey       string
}

type Keysource string
type UpdateType string

const (
	Upstream   Keysource = "Upstream"
	Downstream Keysource = "Downstream"
)

const (
	SpecUpdate     UpdateType = "Spec"
	StatusUpdate   UpdateType = "Status"
	MetadataUpdate UpdateType = "Meta"
)

// Upstream resource key generation
// Add the cluster name/namespace#cluster-name
func getKey(obj interface{}, keySource Keysource) (string, error) {
	if keySource == Upstream {
		return kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	}
	return cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
}

func ParseUpstreamKey(key string) (clusterName, namespace, name string, err error) {
	parts := strings.Split(key, "#")
	clusterName = parts[1]
	namespace, name, err = cache.SplitMetaNamespaceKey(parts[0])
	if err != nil {
		return clusterName, namespace, name, err
	}
	return clusterName, namespace, name, nil
}

// Queue handles keys for both upstream and downstream resources. Key Source identifies if it's a downstream or an upstream object
type queueKey struct {
	gvr schema.GroupVersionResource
	key string
	// Differentiate between upstream and downstream keys
	keysource Keysource
	// Update type
	updateType UpdateType
}

func updateTypeSlicetoString(update []UpdateType) UpdateType {
	str := ""
	for i, updateType := range update {
		if i != 0 {
			str += ","
		}
		str += string(updateType)
	}
	return UpdateType(str)
}

func updateTypeStringtoSlice(update UpdateType) []UpdateType {
	str := strings.Split(string(update), ",")
	updateType := make([]UpdateType, len(str))
	for i, item := range str {
		updateType[i] = UpdateType(item)
	}
	return updateType
}

func getUpdateType(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) []UpdateType {
	newStatus := newObj.UnstructuredContent()["status"]
	oldStatus := oldObj.UnstructuredContent()["status"]
	isStatusUpdated := equality.Semantic.DeepEqual(newStatus, oldStatus)

	newSpec := newObj.UnstructuredContent()["spec"]
	oldSpec := oldObj.UnstructuredContent()["spec"]
	isSpecUpdated := equality.Semantic.DeepEqual(newSpec, oldSpec)

	newMeta := newObj.UnstructuredContent()["metadata"]
	oldMeta := oldObj.UnstructuredContent()["metadata"]
	isMetadataUpdated := equality.Semantic.DeepEqual(newMeta, oldMeta)

	updatedValues := []UpdateType{}
	if isSpecUpdated {
		updatedValues = append(updatedValues, SpecUpdate)
	}
	if isStatusUpdated {
		updatedValues = append(updatedValues, StatusUpdate)
	}
	if isMetadataUpdated {
		updatedValues = append(updatedValues, MetadataUpdate)
	}
	return updatedValues
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger, keysource Keysource, updateType []UpdateType) {
	key, err := getKey(obj, keysource)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing GVR", "gvr", gvr.String())
	updateTypeHashed := updateTypeSlicetoString(updateType)
	c.queue.Add(
		queueKey{
			gvr:        gvr,
			key:        key,
			keysource:  keysource,
			updateType: updateTypeHashed,
		},
	)
}

func NewUpSyncer(syncerLogger logr.Logger, syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	upstreamClient kcpdynamic.ClusterInterface, downstreamClient dynamic.Interface,
	ddsifForUpstreamUpyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncTargetUID types.UID) (*Controller, error) {
	c := &Controller{
		queue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		upstreamClient:   upstreamClient,
		downstreamClient: downstreamClient,
		getDownstreamLister: func(gvr schema.GroupVersionResource) (cache.GenericLister, error) {
			informers, notSynced := ddsifForDownstream.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
			}
			return informer.Lister(), nil
		},
		getUpstreamUpsyncerLister: func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error) {
			informers, notSynced := ddsifForUpstreamUpyncer.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
			}
			return informer.Lister(), nil
		},
		syncTargetName:      syncTargetKey,
		syncTargetWorkspace: syncTargetWorkspace,
		syncTargetUID:       syncTargetUID,
		syncTargetKey:       syncTargetKey,
	}
	logger := logging.WithReconciler(syncerLogger, controllerName)

	namespaceGVR := corev1.SchemeGroupVersion.WithResource("namespaces")

	ddsifForDownstream.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			c.AddToQueue(gvr, obj, logger, Downstream, []UpdateType{})
		},
		UpdateFunc: func(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			updateType := getUpdateType(oldObj.(*unstructured.Unstructured), newObj.(*unstructured.Unstructured))
			if len(updateType) > 0 {
				c.AddToQueue(gvr, newObj, logger, Downstream, updateType)
			}
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			// TODO(davidfestal): do we want to extract the namespace from where the resource was deleted,
			// as done in the SpecController ?
			c.AddToQueue(gvr, obj, logger, Downstream, []UpdateType{})
		},
	})

	ddsifForUpstreamUpyncer.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			c.AddToQueue(gvr, obj, logger, Upstream, []UpdateType{})
		},
	})
	return c, nil
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting upsync workers")
	defer logger.Info("Stopping upsync workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	qk := key.(queueKey)
	ks := qk.keysource
	updateType := qk.updateType
	keySource := "Downstream"
	if ks == Upstream {
		keySource = "Upstream"
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk.key).WithValues("gvr", qk.gvr.String(), "source", keySource)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("Processing key")
	defer c.queue.Done(key)

	updateTypeArray := updateTypeStringtoSlice(updateType)

	if err := c.process(ctx, qk.gvr, qk.key, qk.keysource == Upstream, updateTypeArray); err != nil {
		runtime.HandleError(fmt.Errorf("%s failed to upsync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
