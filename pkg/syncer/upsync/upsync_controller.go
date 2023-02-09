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

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const controllerName = "kcp-resource-upsyncer"

// NewUpSyncer returns a new controller which upsyncs, through the Usyncer virtual workspace, downstream resources
// which are part of the upsyncable resource types (fixed limited list for now), and provide
// the following labels:
//   - internal.workload.kcp.io/cluster: <sync target key>
//   - state.workload.kcp.io/<sync target key>: Upsync
//
// and optionally, for cluster-wide resources, the `kcp.io/namespace-locator` annotation
// filled with the information necessary identify the upstream workspace to upsync to.
func NewUpSyncer(syncerLogger logr.Logger, syncTargetClusterName logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	upstreamClient kcpdynamic.ClusterInterface, downstreamClient dynamic.Interface,
	ddsifForUpstreamUpyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncTargetUID types.UID) (*controller, error) {
	c := &controller{
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
		syncTargetName:        syncTargetName,
		syncTargetClusterName: syncTargetClusterName,
		syncTargetUID:         syncTargetUID,
		syncTargetKey:         syncTargetKey,
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
				runtime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", obj))
				return
			}
			if new.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}
			c.enqueue(gvr, obj, logger, false, new.UnstructuredContent()["status"] != nil)
		},
		UpdateFunc: func(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			old, ok := oldObj.(*unstructured.Unstructured)
			if !ok {
				runtime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", oldObj))
				return
			}
			new, ok := newObj.(*unstructured.Unstructured)
			if !ok {
				runtime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", newObj))
				return
			}
			if new.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}

			oldStatus := old.UnstructuredContent()["status"]
			newStatus := new.UnstructuredContent()["status"]
			isStatusUpdated := newStatus != nil && !equality.Semantic.DeepEqual(oldStatus, newStatus)
			c.enqueue(gvr, newObj, logger, false, isStatusUpdated)
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
				runtime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", unstr))
				return
			}
			if unstr.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}
			// TODO(davidfestal): do we want to extract the namespace from where the resource was deleted,
			// as done in the SpecController ?
			c.enqueue(gvr, obj, logger, false, false)
		},
	})

	ddsifForUpstreamUpyncer.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			c.enqueue(gvr, obj, logger, true, false)
		},
	})
	return c, nil
}

type controller struct {
	queue            workqueue.RateLimitingInterface
	upstreamClient   kcpdynamic.ClusterInterface
	downstreamClient dynamic.Interface

	getDownstreamLister       func(gvr schema.GroupVersionResource) (cache.GenericLister, error)
	getUpstreamUpsyncerLister func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error)

	syncTargetName        string
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
	syncTargetKey         string
}

// Queue handles keys for both upstream and downstream resources.
type queueKey struct {
	gvr schema.GroupVersionResource
	key string
	// indicates whether it's an upstream key
	isUpstream    bool
	includeStatus bool
}

func (c *controller) enqueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger, isUpstream, includeStatus bool) {
	getKey := cache.DeletionHandlingMetaNamespaceKeyFunc
	if isUpstream {
		getKey = kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc
	}
	key, err := getKey(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing GVR", "gvr", gvr.String(), "isUpstream", isUpstream, "includeStatus", includeStatus)
	c.queue.Add(
		queueKey{
			gvr:           gvr,
			key:           key,
			isUpstream:    isUpstream,
			includeStatus: includeStatus,
		},
	)
}

func (c *controller) Start(ctx context.Context, numThreads int) {
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

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	qk := key.(queueKey)
	logger := logging.WithQueueKey(klog.FromContext(ctx), qk.key).WithValues("gvr", qk.gvr.String(), "isUpstream", qk.isUpstream, "includeStatus", qk.includeStatus)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("Processing key")
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key, qk.isUpstream, qk.includeStatus); err != nil {
		runtime.HandleError(fmt.Errorf("%s failed to upsync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
