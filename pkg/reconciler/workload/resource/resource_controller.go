/*
Copyright 2021 The KCP Authors.

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

package resource

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	schedulingv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	ControllerName  = "kcp-workload-resource-scheduler"
	bySyncTargetKey = ControllerName + "bySyncTargetKey"
)

// NewController returns a new Controller which schedules resources in scheduled namespaces.
func NewController(
	dynamicClusterClient kcpdynamic.ClusterInterface,
	ddsif *informer.DynamicDiscoverySharedInformerFactory,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	placementInformer schedulingv1alpha1informers.PlacementClusterInformer,
) (*Controller, error) {
	resourceQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-namespace-resource")
	gvrQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-namespace-gvr")

	c := &Controller{
		resourceQueue: resourceQueue,
		gvrQueue:      gvrQueue,

		dynClusterClient: dynamicClusterClient,

		getNamespace: func(clusterName logicalcluster.Name, namespaceName string) (*corev1.Namespace, error) {
			return namespaceInformer.Lister().Cluster(clusterName).Get(namespaceName)
		},

		getSyncTargetPlacementAnnotations: func(clusterName logicalcluster.Name) (sets.String, error) {
			placements, err := placementInformer.Lister().Cluster(clusterName).List(labels.Everything())
			if err != nil {
				return nil, err
			}

			expectedSyncTargetKeys := sets.String{}
			for _, placement := range placements {
				if val := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]; val != "" {
					expectedSyncTargetKeys.Insert(val)
				}
			}
			return expectedSyncTargetKeys, err
		},

		getSyncTargetFromKey: func(syncTargetKey string) (*workloadv1alpha1.SyncTarget, bool, error) {
			syncTargets, err := indexers.ByIndex[*workloadv1alpha1.SyncTarget](syncTargetInformer.Informer().GetIndexer(), bySyncTargetKey, syncTargetKey)
			if err != nil {
				return nil, false, err
			}
			// This shouldn't happen, more than one SyncTarget with the same key means a hash collision.
			if len(syncTargets) > 1 {
				return nil, false, fmt.Errorf("possible collision: multiple sync targets found for key %q", syncTargetKey)
			}
			if len(syncTargets) == 0 {
				return nil, false, nil
			}
			return syncTargets[0], true, nil
		},

		ddsif: ddsif,
	}

	namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: filterNamespace,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.enqueueNamespace(obj) },
			UpdateFunc: func(old, obj interface{}) {
				oldNS := old.(*corev1.Namespace)
				newNS := obj.(*corev1.Namespace)
				if !reflect.DeepEqual(scheduleStateLabels(oldNS.Labels), scheduleStateLabels(newNS.Labels)) ||
					!reflect.DeepEqual(scheduleStateAnnotations(oldNS.Annotations), scheduleStateAnnotations(newNS.Annotations)) {
					c.enqueueNamespace(obj)
				}

			},
			DeleteFunc: nil, // Nothing to do.
		},
	})

	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueuePlacement,
		UpdateFunc: func(oldObj, _ interface{}) { c.enqueuePlacement(oldObj) },
		DeleteFunc: c.enqueuePlacement,
	})

	ddsif.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc:    func(gvr schema.GroupVersionResource, obj interface{}) { c.enqueueResource(gvr, obj) },
		UpdateFunc: func(gvr schema.GroupVersionResource, _, obj interface{}) { c.enqueueResource(gvr, obj) },
		DeleteFunc: nil, // Nothing to do.
	})

	indexers.AddIfNotPresentOrDie(syncTargetInformer.Informer().GetIndexer(), cache.Indexers{
		bySyncTargetKey: indexBySyncTargetKey,
	})

	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.enqueueSyncTarget(obj)
		},
	})

	return c, nil
}

func scheduleStateLabels(ls map[string]string) map[string]string {
	ret := make(map[string]string, len(ls))
	for k, v := range ls {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			ret[k] = v
		}
	}
	return ret
}

func scheduleStateAnnotations(ls map[string]string) map[string]string {
	ret := make(map[string]string, len(ls))
	for k, v := range ls {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix) {
			ret[k] = v
		}
	}
	return ret
}

type Controller struct {
	resourceQueue workqueue.RateLimitingInterface
	gvrQueue      workqueue.RateLimitingInterface

	dynClusterClient kcpdynamic.ClusterInterface

	getNamespace                      func(clusterName logicalcluster.Name, namespaceName string) (*corev1.Namespace, error)
	getSyncTargetPlacementAnnotations func(clusterName logicalcluster.Name) (sets.String, error)
	getSyncTargetFromKey              func(syncTargetKey string) (*workloadv1alpha1.SyncTarget, bool, error)

	ddsif *informer.DynamicDiscoverySharedInformerFactory
}

func filterNamespace(obj interface{}) bool {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	_, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	if namespaceBlocklist.Has(name) {
		logging.WithReconciler(klog.Background(), ControllerName).WithValues("namespace", name).V(2).Info("skipping syncing Namespace")
		return false
	}
	return true
}

func (c *Controller) enqueueResource(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	queueKey := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".") + "::" + key
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), queueKey)
	logger.V(2).Info("queueing resource")
	c.resourceQueue.Add(queueKey)
}

func (c *Controller) enqueueGVR(gvr schema.GroupVersionResource) {
	queueKey := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), queueKey)
	logger.V(2).Info("queueing GVR")
	c.gvrQueue.Add(queueKey)
}

func (c *Controller) enqueueNamespace(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, err := c.getNamespace(clusterName, name)
	if err != nil {
		if errors.IsNotFound(err) {
			// Namespace was deleted
			return
		}

		runtime.HandleError(err)
		return
	}
	if err := c.enqueueResourcesForNamespace(ns); err != nil {
		runtime.HandleError(err)
		return
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.resourceQueue.ShutDown()
	defer c.gvrQueue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startResourceWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startGVRWorker(ctx) }, time.Second, ctx.Done())
	}
	<-ctx.Done()
}

func (c *Controller) startResourceWorker(ctx context.Context) {
	for processNext(ctx, c.resourceQueue, c.processResource) {
	}
}
func (c *Controller) startGVRWorker(ctx context.Context) {
	for processNext(ctx, c.gvrQueue, c.processGVR) {
	}
}

func processNext(
	ctx context.Context,
	queue workqueue.RateLimitingInterface,
	processFunc func(ctx context.Context, key string) error,
) bool {
	// Wait until there is a new item in the working queue
	k, quit := queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer queue.Done(key)

	if err := processFunc(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		queue.AddRateLimited(key)
		return true
	}
	queue.Forget(key)
	return true
}

// key is gvr::KEY
func (c *Controller) processResource(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		logger.Info("error parsing key; dropping")
		return nil
	}
	gvrstr := parts[0]
	logger = logger.WithValues("gvr", gvrstr)
	gvr, _ := schema.ParseResourceArg(gvrstr)
	if gvr == nil {
		logger.Info("error parsing GVR; dropping")
		return nil
	}
	key = parts[1]
	logger = logger.WithValues("objectKey", key)

	inf, err := c.ddsif.ForResource(*gvr)
	if err != nil {
		return err
	}

	lclusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}
	obj, err := inf.Lister().ByCluster(lclusterName).ByNamespace(namespace).Get(name)
	if err != nil {
		logger.Error(err, "error getting object from indexer")
		return err
	}
	unstr, ok := obj.(*unstructured.Unstructured)
	if !ok {
		logger.WithValues("objectType", fmt.Sprintf("%T", obj)).Info("object was not Unstructured, dropping")
		return nil
	}
	unstr = unstr.DeepCopy()

	return c.reconcileResource(ctx, lclusterName, unstr, gvr)
}

func (c *Controller) processGVR(ctx context.Context, gvrstr string) error {
	logger := klog.FromContext(ctx).WithValues("gvr", gvrstr)
	gvr, _ := schema.ParseResourceArg(gvrstr)
	if gvr == nil {
		logger.Info("error parsing GVR; dropping")
		return nil
	}
	return c.reconcileGVR(*gvr)
}

// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
var namespaceBlocklist = sets.NewString("kube-system", "kube-public", "kube-node-lease", apiexport.DefaultIdentitySecretNamespace)

// enqueueResourcesForNamespace adds the resources contained by the given
// namespace to the queue if there scheduling label differs from the namespace's.
func (c *Controller) enqueueResourcesForNamespace(ns *corev1.Namespace) error {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), ns).WithValues("operation", "enqueueResourcesForNamespace")
	clusterName := logicalcluster.From(ns)

	nsLocations := getLocations(ns.Labels, true)
	nsDeleting := getDeletingLocations(ns.Annotations)
	logger = logger.WithValues("nsLocations", nsLocations.List())

	logger.V(4).Info("getting listers")
	listers, notSynced := c.ddsif.Listers()
	var errs []error
	for gvr, lister := range listers {
		logger = logger.WithValues("gvr", gvr.String())
		objs, err := lister.ByCluster(clusterName).ByNamespace(ns.Name).List(labels.Everything())
		if err != nil {
			errs = append(errs, fmt.Errorf("error listing %q in %s|%s: %w", gvr, clusterName, ns.Name, err))
			continue
		}

		logger.WithValues("items", len(objs)).V(4).Info("got items for GVR")

		var enqueuedResources []string
		for _, obj := range objs {
			u := obj.(*unstructured.Unstructured)

			objLocations := getLocations(u.GetLabels(), false)
			objDeleting := getDeletingLocations(u.GetAnnotations())
			logger := logging.WithObject(logger, u).WithValues("gvk", gvr.GroupVersion().WithKind(u.GetKind()))
			if !objLocations.Equal(nsLocations) || !reflect.DeepEqual(objDeleting, nsDeleting) {
				c.enqueueResource(gvr, obj)

				if klog.V(2).Enabled() && !klog.V(4).Enabled() && len(enqueuedResources) < 10 {
					enqueuedResources = append(enqueuedResources, u.GetName())
				}

				logger.V(3).Info("enqueuing object to schedule it")
			} else {
				logger.V(4).Info("skipping object as it is already correctly scheduled")
			}
		}

		if len(enqueuedResources) > 0 {
			if len(enqueuedResources) == 10 {
				enqueuedResources = append(enqueuedResources, "...")
			}
			logger.WithValues("resources", enqueuedResources).V(2).Info("enqueuing resources for GVR")
		}
	}

	// For all types whose informer hasn't synced yet, enqueue a workqueue
	// item to check that GVR again later (reconcileGVR, above).
	for _, gvr := range notSynced {
		logger.V(3).Info("informer for GVR is not synced, needed for namespace; re-enqueueing")
		c.enqueueGVR(gvr)
	}

	return utilerrors.NewAggregate(errs)
}

func (c *Controller) enqueueSyncTarget(obj interface{}) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		runtime.HandleError(fmt.Errorf("object is not a metav1.Object: %T", obj))
		return
	}
	clusterName := logicalcluster.From(metaObj)
	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(clusterName, metaObj.GetName())

	c.enqueueSyncTargetKey(syncTargetKey)
}

func (c *Controller) enqueueSyncTargetKey(syncTargetKey string) {
	logger := logging.WithReconciler(klog.Background(), ControllerName).WithValues("syncTargetKey", syncTargetKey)

	listers, _ := c.ddsif.Listers()
	queued := map[string]int{}
	for gvr := range listers {
		inf, err := c.ddsif.ForResource(gvr)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		stateLabelObjs, err := inf.Informer().GetIndexer().ByIndex(indexers.ByClusterResourceStateLabelKey, workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		syncerFinalizerObjs, err := inf.Informer().GetIndexer().ByIndex(indexers.BySyncerFinalizerKey, shared.SyncerFinalizerNamePrefix+syncTargetKey)
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		// let's deduplicate the objects from both indexes.
		inObjs := make(map[types.UID]bool)
		var objs []interface{}
		for _, obj := range append(stateLabelObjs, syncerFinalizerObjs...) {
			obj, ok := obj.(*unstructured.Unstructured)
			if !ok {
				runtime.HandleError(fmt.Errorf("object is not an *unstructured.Unstructured: %T", obj))
				continue
			}
			if _, ok := inObjs[obj.GetUID()]; !ok {
				inObjs[obj.GetUID()] = true
				objs = append(objs, obj)
			}
		}

		if len(objs) == 0 {
			continue
		}
		for _, obj := range objs {
			c.enqueueResource(gvr, obj)
		}
		queued[gvr.String()] = len(objs)
	}
	if len(queued) > 0 {
		logger.WithValues("syncTargetKey", syncTargetKey, "resources", queued).V(2).Info("queued GVRs assigned to a syncTargetKey because SyncTarget or Placement changed.")
	}
}

// getLocations returns a set with of all the locations extracted from a resource labels, setting skipPending to true will ignore resources in not Sync state.
func getLocations(labels map[string]string, skipPending bool) sets.String {
	locations := sets.NewString()
	for k, v := range labels {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) && (!skipPending || v == string(workloadv1alpha1.ResourceStateSync)) {
			locations.Insert(strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix))
		}
	}
	return locations
}

// getDeletingLocations returns a map of synctargetkeys that are being deleted with the value being the deletion timestamp.
func getDeletingLocations(annotations map[string]string) map[string]string {
	deletingLocations := make(map[string]string)
	for k, v := range annotations {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix) {
			deletingLocations[strings.TrimPrefix(k, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix)] = v
		}
	}
	return deletingLocations
}

func (c *Controller) enqueuePlacement(obj interface{}) {
	_, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	placement, ok := obj.(*schedulingv1alpha1.Placement)
	if !ok {
		runtime.HandleError(fmt.Errorf("expected a Placement, got a %T", obj))
		return
	}
	syncTargetKey := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]
	if syncTargetKey == "" {
		return
	}

	c.enqueueSyncTargetKey(syncTargetKey)
}

func indexBySyncTargetKey(obj interface{}) ([]string, error) {
	syncTarget, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a syncTarget, but is %T", obj)
	}

	if _, ok := syncTarget.GetLabels()[workloadv1alpha1.InternalSyncTargetKeyLabel]; !ok {
		return []string{}, nil
	}

	return []string{syncTarget.GetLabels()[workloadv1alpha1.InternalSyncTargetKeyLabel]}, nil
}
