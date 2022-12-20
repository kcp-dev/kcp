/*
Copyright 2022 The KCP Authors.

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

package resourcesync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "kcp-syncer-resourcesync-controller"
)

type SyncerInformer struct {
	UpstreamInformer   kcpkubernetesinformers.GenericClusterInformer
	DownstreamInformer informers.GenericInformer
	cancel             context.CancelFunc
}

type SyncerInformerFactory interface {
	AddUpstreamEventHandler(handler ResourceEventHandlerPerGVR)
	AddDownstreamEventHandler(handler ResourceEventHandlerPerGVR)
	InformerForResource(gvr schema.GroupVersionResource) (*SyncerInformer, bool)
	SyncableGVRs() (map[schema.GroupVersionResource]*SyncerInformer, error)
	Start(ctx context.Context, numThreads int)
}

type ResourceEventHandlerPerGVR func(schema.GroupVersionResource) cache.ResourceEventHandler

// controller is a control loop that watches synctarget. It starts/stops spec syncer and status syncer
// per gvr based on synctarget.Status.SyncedResources.
// All the spec/status syncer share the same downstreamNSInformer and upstreamSecretInformer. Informers
// for gvr is started separated for each syncer.
type Controller struct {
	queue                        workqueue.RateLimitingInterface
	upstreamDynamicClusterClient kcpdynamic.ClusterInterface
	downstreamDynamicClient      dynamic.Interface
	downstreamKubeClient         kubernetes.Interface

	upstreamEventHandlers   []ResourceEventHandlerPerGVR
	downstreamEventHandlers []ResourceEventHandlerPerGVR

	syncTargetName        string
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
	syncTargetLister      workloadv1alpha1listers.SyncTargetLister
	kcpClient             clientset.Interface

	syncerInformerMap map[schema.GroupVersionResource]*SyncerInformer
	mutex             sync.RWMutex
}

func NewController(
	syncerLogger logr.Logger,
	upstreamDynamicClusterClient kcpdynamic.ClusterInterface,
	downstreamDynamicClient dynamic.Interface,
	downstreamKubeClient kubernetes.Interface,
	kcpClient clientset.Interface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	syncTargetName string,
	syncTargetClusterName logicalcluster.Name,
	syncTargetUID types.UID,
) (SyncerInformerFactory, error) {
	c := &Controller{
		queue:                        workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		upstreamDynamicClusterClient: upstreamDynamicClusterClient,
		downstreamDynamicClient:      downstreamDynamicClient,
		downstreamKubeClient:         downstreamKubeClient,
		kcpClient:                    kcpClient,
		upstreamEventHandlers:        []ResourceEventHandlerPerGVR{},
		downstreamEventHandlers:      []ResourceEventHandlerPerGVR{},
		syncerInformerMap:            map[schema.GroupVersionResource]*SyncerInformer{},
		syncTargetName:               syncTargetName,
		syncTargetClusterName:        syncTargetClusterName,
		syncTargetUID:                syncTargetUID,
		syncTargetLister:             syncTargetInformer.Lister(),
	}

	logger := logging.WithReconciler(syncerLogger, controllerName)

	syncTargetInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, name, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				return false
			}
			return name == syncTargetName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
			UpdateFunc: func(old, obj interface{}) { c.enqueueSyncTarget(obj, logger) },
			DeleteFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
		},
	})

	return c, nil
}

func (c *Controller) enqueueSyncTarget(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing SyncTarget")

	c.queue.Add(key)
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) AddUpstreamEventHandler(handler ResourceEventHandlerPerGVR) {
	c.upstreamEventHandlers = append(c.upstreamEventHandlers, handler)
}

func (c *Controller) AddDownstreamEventHandler(handler ResourceEventHandlerPerGVR) {
	c.downstreamEventHandlers = append(c.downstreamEventHandlers, handler)
}

func (c *Controller) InformerForResource(gvr schema.GroupVersionResource) (*SyncerInformer, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	if informer, ok := c.syncerInformerMap[gvr]; ok {
		return informer, true
	}

	return nil, false
}

func (c *Controller) SyncableGVRs() (map[schema.GroupVersionResource]*SyncerInformer, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.syncerInformerMap, nil
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}

	syncTarget, err := c.syncTargetLister.Get(name)
	if apierrors.IsNotFound(err) {
		c.stopUnusedSyncerInformers(ctx, map[schema.GroupVersionResource]bool{})
		return nil
	}

	if err != nil {
		return err
	}

	if syncTarget.GetUID() != c.syncTargetUID {
		return nil
	}

	requiredGVRs := getAllGVRs(syncTarget)

	var errs []error
	var unauthorizedGVRs []string
	for gvr := range requiredGVRs {
		logger := logger.WithValues("gvr", gvr.String())
		ctx := klog.NewContext(ctx, logger)
		allowed, err := c.checkSSAR(ctx, gvr)
		if err != nil {
			logger.Error(err, "Failed to check ssar")
			errs = append(errs, err)
			unauthorizedGVRs = append(unauthorizedGVRs, gvr.String())
			continue
		}

		if !allowed {
			logger.V(2).Info("Stop informer since the syncer is not authorized to sync")
			// remove this from requiredGVRs so its informer will be stopped later.
			delete(requiredGVRs, gvr)
			unauthorizedGVRs = append(unauthorizedGVRs, gvr.String())
			continue
		}

		c.startSyncerInformer(ctx, gvr, syncTarget)
	}

	c.stopUnusedSyncerInformers(ctx, requiredGVRs)

	newSyncTarget := syncTarget.DeepCopy()

	if len(unauthorizedGVRs) > 0 {
		conditions.MarkFalse(
			newSyncTarget,
			workloadv1alpha1.SyncerAuthorized,
			"SyncerUnauthorized",
			conditionsv1alpha1.ConditionSeverityError,
			"SSAR check failed for gvrs: %s", strings.Join(unauthorizedGVRs, ";"),
		)
	} else {
		conditions.MarkTrue(newSyncTarget, workloadv1alpha1.SyncerAuthorized)
	}

	if err := c.patchSyncTargetCondition(ctx, newSyncTarget, syncTarget); err != nil {
		errs = append(errs, err)
	}

	return errors.NewAggregate(errs)
}

func (c *Controller) patchSyncTargetCondition(ctx context.Context, new, old *workloadv1alpha1.SyncTarget) error {
	logger := klog.FromContext(ctx)
	// If the object being reconciled changed as a result, update it.
	if equality.Semantic.DeepEqual(old.Status.Conditions, new.Status.Conditions) {
		return nil
	}
	oldData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		Status: workloadv1alpha1.SyncTargetStatus{
			Conditions: old.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for syncTarget %s: %w", old.Name, err)
	}

	newData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			UID:             old.UID,
			ResourceVersion: old.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: workloadv1alpha1.SyncTargetStatus{
			Conditions: new.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for syncTarget %s: %w", new.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for syncTarget %s: %w", new.Name, err)
	}
	logger.V(2).Info("patching syncTarget", "patch", string(patchBytes))
	_, uerr := c.kcpClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return uerr
}

func (c *Controller) checkSSAR(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	ssar := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    gvr.Group,
				Resource: gvr.Resource,
				Version:  gvr.Version,
				Verb:     "*",
			},
		},
	}

	sar, err := c.downstreamKubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}

	return sar.Status.Allowed, nil
}

// stopUnusedSyncerInformers stop syncers for gvrs not in requiredGVRs
func (c *Controller) stopUnusedSyncerInformers(ctx context.Context, requiredGVRs map[schema.GroupVersionResource]bool) {
	logger := klog.FromContext(ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	for gvr, informer := range c.syncerInformerMap {
		if _, ok := requiredGVRs[gvr]; !ok {
			logger.WithValues("gvr", gvr.String()).V(2).Info("Stop syncer for gvr")
			informer.cancel()
			delete(c.syncerInformerMap, gvr)
		}
	}
}

func (c *Controller) startSyncerInformer(ctx context.Context, gvr schema.GroupVersionResource, syncTarget *workloadv1alpha1.SyncTarget) {
	logger := klog.FromContext(ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, ok := c.syncerInformerMap[gvr]; ok {
		logger.V(2).Info("Informer is started already")
		return
	}

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(c.syncTargetClusterName, c.syncTargetName)

	upstreamInformer := kcpdynamicinformer.NewFilteredDynamicInformer(c.upstreamDynamicClusterClient, gvr, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
		kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc}, func(o *metav1.ListOptions) {},
	)
	downstreamInformer := dynamicinformer.NewFilteredDynamicInformer(c.downstreamDynamicClient, gvr, metav1.NamespaceAll, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
	})

	for _, handler := range c.upstreamEventHandlers {
		upstreamInformer.Informer().AddEventHandler(handler(gvr))
	}

	for _, handler := range c.downstreamEventHandlers {
		downstreamInformer.Informer().AddEventHandler(handler(gvr))
	}

	logger.V(2).Info("Start informer for gvr")
	syncerCtx, cancel := context.WithCancel(ctx)

	go downstreamInformer.Informer().Run(syncerCtx.Done())
	go upstreamInformer.Informer().Run(syncerCtx.Done())

	c.syncerInformerMap[gvr] = &SyncerInformer{
		cancel:             cancel,
		UpstreamInformer:   upstreamInformer,
		DownstreamInformer: downstreamInformer,
	}
}

func getAllGVRs(synctarget *workloadv1alpha1.SyncTarget) map[schema.GroupVersionResource]bool {
	// TODO(jmprusi): Added Configmaps and Secrets to the default syncing, but we should figure out
	//                a way to avoid doing that: https://github.com/kcp-dev/kcp/issues/727
	gvrs := map[schema.GroupVersionResource]bool{
		{
			Version:  "v1",
			Resource: "configmaps",
		}: true,
		{
			Version:  "v1",
			Resource: "secrets",
		}: true,
	}

	// TODO(qiujian16) We currently checks the API compaibility on the server side. When we change to check the
	// compatibility on the syncer side, this part needs to be changed.
	for _, r := range synctarget.Status.SyncedResources {
		if r.State != workloadv1alpha1.ResourceSchemaAcceptedState {
			continue
		}
		for _, version := range r.Versions {
			gvrs[schema.GroupVersionResource{
				Group:    r.Group,
				Version:  version,
				Resource: r.Resource,
			}] = true
		}
	}

	return gvrs
}
