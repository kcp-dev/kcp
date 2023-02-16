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
	"sync"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncerindexers "github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const controllerName = "kcp-resource-upsyncer"

var namespaceGVR schema.GroupVersionResource = corev1.SchemeGroupVersion.WithResource("namespaces")

// NewUpSyncer returns a new controller which upsyncs, through the Upsyncer virtual workspace, downstream resources
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
	ddsifForUpstreamUpsyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
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
		listDownstreamNamespacesByLocator: func(jsonLocator string) ([]*unstructured.Unstructured, error) {
			nsInformer, err := ddsifForDownstream.ForResource(namespaceGVR)
			if err != nil {
				return nil, err
			}
			return indexers.ByIndex[*unstructured.Unstructured](nsInformer.Informer().GetIndexer(), syncerindexers.ByNamespaceLocatorIndexName, jsonLocator)
		},
		getUpstreamUpsyncerLister: func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error) {
			informers, notSynced := ddsifForUpstreamUpsyncer.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the upstream upsyncer informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream upstream upsyncer informer factory", gvr)
			}
			return informer.Lister(), nil
		},
		getUpsyncedGVRs: func() ([]schema.GroupVersionResource, error) {
			informers, notSynced := ddsifForUpstreamUpsyncer.Informers()
			var result []schema.GroupVersionResource
			for k := range informers {
				result = append(result, k)
			}
			if len(notSynced) > 0 {
				return result, fmt.Errorf("informers not synced in the upstream upsyncer informer factory for gvrs %v", notSynced)
			}
			return result, nil
		},

		syncTargetName:        syncTargetName,
		syncTargetClusterName: syncTargetClusterName,
		syncTargetUID:         syncTargetUID,
		syncTargetKey:         syncTargetKey,
	}
	logger := logging.WithReconciler(syncerLogger, controllerName)

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
			c.enqueueDownstream(gvr, new, logger, new.UnstructuredContent()["status"] != nil)
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
			c.enqueueDownstream(gvr, new, logger, newStatus != nil && !equality.Semantic.DeepEqual(oldStatus, newStatus))
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			unstr, ok := obj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", unstr))
				return
			}

			if gvr == namespaceGVR {
				c.enqueueDeletedDownstreamNamespace(unstr, logger)
				return
			}

			if unstr.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != string(workloadv1alpha1.ResourceStateUpsync) {
				return
			}
			c.enqueueDownstream(gvr, unstr, logger, false)
		},
	})

	ddsifForUpstreamUpsyncer.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			unstr, ok := obj.(*unstructured.Unstructured)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a *unstructured.Unstructured, but was %T", unstr))
				return
			}
			if unstr.GetAnnotations()[ResourceVersionAnnotation] == "" {
				// The upstream resource hasn't been completely upsynced, and is being created.
				// Cleanup doesn't make sense here.
				return
			}
			c.enqueueUpstream(gvr, obj, logger, false)
		},
	})
	return c, nil
}

type controller struct {
	queue           workqueue.RateLimitingInterface
	dirtyStatusKeys sync.Map

	upstreamClient   kcpdynamic.ClusterInterface
	downstreamClient dynamic.Interface

	getDownstreamLister               func(gvr schema.GroupVersionResource) (cache.GenericLister, error)
	listDownstreamNamespacesByLocator func(jsonLocator string) ([]*unstructured.Unstructured, error)
	getUpstreamUpsyncerLister         func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error)
	getUpsyncedGVRs                   func() ([]schema.GroupVersionResource, error)

	syncTargetName        string
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
	syncTargetKey         string
}

// queueKey is a composite queue key that combines the gvr and the key of the upstream
// resource that should be reconciled.
type queueKey struct {
	gvr schema.GroupVersionResource
	// key is the cluster-aware cache key of the upstream resource
	key string
}

func (c *controller) enqueueUpstream(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger, dirtyStatus bool) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing", "gvr", gvr.String())
	queueKey := queueKey{
		gvr: gvr,
		key: key,
	}

	if dirtyStatus {
		c.dirtyStatusKeys.Store(queueKey, true)
	}
	c.queue.Add(queueKey)
}

func (c *controller) enqueueDeletedDownstreamNamespace(deletedNamespace *unstructured.Unstructured, logger logr.Logger) {
	upstreamLocator, locatorExists, err := shared.LocatorFromAnnotations(deletedNamespace.GetAnnotations())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	if !locatorExists || upstreamLocator == nil {
		logger.V(4).Info("the namespace locator doesn't exist on deleted downstream namespace.")
		return
	}

	if upstreamLocator.SyncTarget.UID != c.syncTargetUID || upstreamLocator.SyncTarget.ClusterName != c.syncTargetClusterName.String() {
		return
	}

	upsyncedGVRs, err := c.getUpsyncedGVRs()
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, gvr := range upsyncedGVRs {
		upstreamLister, err := c.getUpstreamUpsyncerLister(gvr)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}

		upstreamUpsyncedResources, err := upstreamLister.ByCluster(upstreamLocator.ClusterName).ByNamespace(upstreamLocator.Namespace).List(labels.Everything())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}

		for _, upstreamUpsyncedResource := range upstreamUpsyncedResources {
			c.enqueueUpstream(gvr, upstreamUpsyncedResource, logger, false)
		}
	}
}

func (c *controller) enqueueDownstream(gvr schema.GroupVersionResource, downstreamObj *unstructured.Unstructured, logger logr.Logger, dirtyStatus bool) {
	downstreamNamespace := downstreamObj.GetNamespace()
	locatorHolder := downstreamObj
	if downstreamNamespace != "" {
		// get locator from namespace for namespaced objects
		downstreamNamespaceLister, err := c.getDownstreamLister(namespaceGVR)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		nsObj, err := downstreamNamespaceLister.Get(downstreamNamespace)
		if errors.IsNotFound(err) {
			logger.V(4).Info("the downstream namespace doesn't exist anymore.")
			return
		}
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		if unstr, ok := nsObj.(*unstructured.Unstructured); !ok {
			utilruntime.HandleError(fmt.Errorf("downstream ns expected to be *unstructured.Unstructured got %T", nsObj))
			return
		} else {
			locatorHolder = unstr
		}
	}

	upstreamLocator, locatorExists, err := shared.LocatorFromAnnotations(locatorHolder.GetAnnotations())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	if !locatorExists || upstreamLocator == nil {
		logger.V(4).Info("the namespace locator doesn't exist on downstream namespace.")
		return
	}

	if upstreamLocator.SyncTarget.UID != c.syncTargetUID || upstreamLocator.SyncTarget.ClusterName != c.syncTargetClusterName.String() {
		return
	}

	c.enqueueUpstream(gvr, &metav1.ObjectMeta{
		Name:      downstreamObj.GetName(),
		Namespace: upstreamLocator.Namespace,
		Annotations: map[string]string{
			logicalcluster.AnnotationKey: upstreamLocator.ClusterName.String(),
		},
	}, logging.WithObject(logger, downstreamObj), dirtyStatus)
}

func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(queueKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key.key).WithValues("gvr", key.gvr)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key.key, key.gvr); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q (%s), err: %w", controllerName, key.key, key.gvr.String(), err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string, gvr schema.GroupVersionResource) (bool, error) {
	logger := klog.FromContext(ctx)

	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}

	dirtyStatus := false
	if dirtyStatusObj, found := c.dirtyStatusKeys.LoadAndDelete(queueKey{
		gvr: gvr,
		key: key,
	}); found {
		dirtyStatus = dirtyStatusObj.(bool)
	}

	resetDirty := func(requeue bool, err error) (bool, error) {
		if dirtyStatus && err != nil {
			c.dirtyStatusKeys.Store(queueKey{
				gvr: gvr,
				key: key,
			}, true)
		}
		return requeue, err
	}

	upstreamLister, err := c.getUpstreamUpsyncerLister(gvr)
	if err != nil {
		return resetDirty(false, err)
	}
	getter := upstreamLister.ByCluster(clusterName).Get
	if namespace != "" {
		getter = upstreamLister.ByCluster(clusterName).ByNamespace(namespace).Get
	}

	upstreamObj, err := getter(name)
	if err != nil && !errors.IsNotFound(err) {
		return resetDirty(false, err)
	}

	var upstreamResource *unstructured.Unstructured
	if upstreamObj != nil {
		var ok bool
		upstreamResource, ok = upstreamObj.(*unstructured.Unstructured)
		if !ok {
			logger.Error(nil, "got unexpected type", "type", fmt.Sprintf("%T", upstreamObj))
			return false, nil // retrying won't help
		}
	}
	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NamespaceKey, namespace, logging.NameKey, name)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, upstreamResource, gvr, clusterName, namespace, name, dirtyStatus)
	if err != nil {
		errs = append(errs, err)
	}

	return resetDirty(requeue, utilerrors.NewAggregate(errs))
}
