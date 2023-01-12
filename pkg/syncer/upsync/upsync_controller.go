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
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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

// Upstream resource key generation
// Add the cluster name/namespace#cluster-name.
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

// Queue handles keys for both upstream and downstream resources. Key Source identifies if it's a downstream or an upstream object.
type queueKey struct {
	gvr schema.GroupVersionResource
	key string
	// Differentiate between upstream and downstream keys
	keysource     Keysource
	includeStatus bool
}

func (c *Controller) enqueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger, keysource Keysource, includeStatus bool) {
	key, err := getKey(obj, keysource)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing GVR", "gvr", gvr.String(), "keySource", keysource, "includeStatus", includeStatus)
	c.queue.Add(
		queueKey{
			gvr:           gvr,
			key:           key,
			keysource:     keysource,
			includeStatus: includeStatus,
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
					return nil, fmt.Errorf("informer for gvr %v not synced in the upstream upsyncer informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream upstream upsyncer informer factory", gvr)
			}
			return informer.Lister(), nil
		},
		syncTargetName:      syncTargetName,
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
			new, ok := obj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", obj))
				return
			}
			if new.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}
			c.enqueue(gvr, obj, logger, Downstream, new.UnstructuredContent()["status"] != nil)
		},
		UpdateFunc: func(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			old, ok := oldObj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", oldObj))
				return
			}
			new, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", newObj))
				return
			}
			if new.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}

			oldStatus := old.UnstructuredContent()["status"]
			newStatus := new.UnstructuredContent()["status"]
			isStatusUpdated := oldStatus != nil && newStatus != nil && equality.Semantic.DeepEqual(oldStatus, newStatus)
			c.enqueue(gvr, newObj, logger, Downstream, isStatusUpdated)
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			unstr, ok := obj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", unstr))
				return
			}
			if unstr.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}
			// TODO(davidfestal): do we want to extract the namespace from where the resource was deleted,
			// as done in the SpecController ?
			c.enqueue(gvr, obj, logger, Downstream, false)
		},
	})

	ddsifForUpstreamUpyncer.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			c.enqueue(gvr, obj, logger, Upstream, false)
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
	logger := logging.WithQueueKey(klog.FromContext(ctx), qk.key).WithValues("gvr", qk.gvr.String(), "keySource", qk.keysource, "includeStatus", qk.includeStatus)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("Processing key")
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key, qk.keysource == Upstream, qk.includeStatus); err != nil {
		runtime.HandleError(fmt.Errorf("%s failed to upsync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
