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
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
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
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "kcp-syncer-resourcesync-controller"
)

var _ informer.GVRSource = (*Controller)(nil)

// controller is a control loop that watches synctarget. It starts/stops spec syncer and status syncer
// per gvr based on synctarget.Status.SyncedResources.
// All the spec/status syncer share the same downstreamNSInformer and upstreamSecretInformer. Informers
// for gvr is started separated for each syncer.
type Controller struct {
	queue                workqueue.RateLimitingInterface
	downstreamKubeClient kubernetes.Interface

	cachedDiscovery discovery.CachedDiscoveryInterface

	syncTargetUID               types.UID
	syncTargetLister            workloadv1alpha1listers.SyncTargetLister
	synctargetInformerCacheSync cache.InformerSynced
	kcpClient                   clientset.Interface

	gvrs map[schema.GroupVersionResource]informer.GVRPartialMetadata

	mutex sync.RWMutex

	// Support subscribers that want to know when Synced GVRs have changed.
	subscribersLock sync.Mutex
	subscribers     []chan<- struct{}
}

func NewController(
	syncerLogger logr.Logger,
	discoveryClient discovery.DiscoveryInterface,
	upstreamDynamicClusterClient kcpdynamic.ClusterInterface,
	downstreamDynamicClient dynamic.Interface,
	downstreamKubeClient kubernetes.Interface,
	kcpClient clientset.Interface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	syncTargetName string,
	syncTargetClusterName logicalcluster.Name,
	syncTargetUID types.UID,
) (*Controller, error) {
	c := &Controller{
		queue:                       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		cachedDiscovery:             memory.NewMemCacheClient(discoveryClient),
		downstreamKubeClient:        downstreamKubeClient,
		kcpClient:                   kcpClient,
		syncTargetUID:               syncTargetUID,
		syncTargetLister:            syncTargetInformer.Lister(),
		synctargetInformerCacheSync: syncTargetInformer.Informer().HasSynced,
		gvrs:                        map[schema.GroupVersionResource]informer.GVRPartialMetadata{},
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
		return c.removeUnusedGVRs(ctx, map[schema.GroupVersionResource]bool{})
	}

	if err != nil {
		return err
	}

	if syncTarget.GetUID() != c.syncTargetUID {
		return nil
	}

	requiredGVRs := getAllGVRs(syncTarget)

	c.cachedDiscovery.Invalidate()

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

		if err := c.addGVR(ctx, gvr, syncTarget); err != nil {
			return err
		}
	}

	if err := c.removeUnusedGVRs(ctx, requiredGVRs); err != nil {
		return err
	}

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

// removeUnusedGVRs stop syncers for gvrs not in requiredGVRs.
func (c *Controller) removeUnusedGVRs(ctx context.Context, requiredGVRs map[schema.GroupVersionResource]bool) error {
	logger := klog.FromContext(ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	updated := false
	for gvr := range c.gvrs {
		if _, ok := requiredGVRs[gvr]; !ok {
			logger.WithValues("gvr", gvr.String()).V(2).Info("Stop syncer for gvr")
			delete(c.gvrs, gvr)
			updated = true
		}
	}

	if updated {
		c.notifySubscribers(ctx)
	}
	return nil
}

func (c *Controller) GVRs() map[schema.GroupVersionResource]informer.GVRPartialMetadata {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	gvrs := make(map[schema.GroupVersionResource]informer.GVRPartialMetadata, len(c.gvrs)+len(builtinGVRs)+1)
	gvrs[corev1.SchemeGroupVersion.WithResource("namespaces")] = informer.GVRPartialMetadata{
		Scope: apiextensionsv1.ClusterScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "namespace",
			Kind:     "Namespace",
		},
	}
	for key, value := range builtinGVRs {
		gvrs[key] = value
	}
	for key, value := range c.gvrs {
		gvrs[key] = value
	}
	return gvrs
}

func (c *Controller) getGVRPartialMetadata(gvr schema.GroupVersionResource) (*informer.GVRPartialMetadata, error) {
	apiResourceList, err := c.cachedDiscovery.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return nil, err
	}
	for _, apiResource := range apiResourceList.APIResources {
		if apiResource.Name == gvr.Resource {
			var resourceScope apiextensionsv1.ResourceScope
			if apiResource.Namespaced {
				resourceScope = apiextensionsv1.NamespaceScoped
			} else {
				resourceScope = apiextensionsv1.ClusterScoped
			}

			return &informer.GVRPartialMetadata{
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind:     apiResource.Kind,
						Singular: apiResource.SingularName,
					},
					Scope: resourceScope,
				},
				nil
		}
	}
	return nil, fmt.Errorf("unable to retrieve discovery for GVR: %s", gvr)
}

func (c *Controller) Ready() bool {
	return c.synctargetInformerCacheSync()
}

func (c *Controller) Subscribe() <-chan struct{} {
	c.subscribersLock.Lock()
	defer c.subscribersLock.Unlock()

	// Use a buffered channel so we can always send at least 1, regardless of consumer status.
	changes := make(chan struct{}, 1)
	c.subscribers = append(c.subscribers, changes)

	return changes
}

func (c *Controller) notifySubscribers(ctx context.Context) {
	logger := klog.FromContext(ctx)

	c.subscribersLock.Lock()
	defer c.subscribersLock.Unlock()

	for index, ch := range c.subscribers {
		logger.V(4).Info("Attempting to notify subscribers", "index", index)
		select {
		case ch <- struct{}{}:
			logger.V(4).Info("Successfully notified subscriber", "index", index)
		default:
			logger.V(4).Info("Unable to notify subscriber - channel full", "index", index)
		}
	}
}

func (c *Controller) addGVR(ctx context.Context, gvr schema.GroupVersionResource, syncTarget *workloadv1alpha1.SyncTarget) error {
	logger := klog.FromContext(ctx)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if _, ok := c.gvrs[gvr]; ok {
		logger.V(2).Info("Informer is started already")
		return nil
	}

	partialMetadata, err := c.getGVRPartialMetadata(gvr)
	if err != nil {
		return err
	}

	c.gvrs[gvr] = *partialMetadata

	c.notifySubscribers(ctx)
	return nil
}

var builtinGVRs = map[schema.GroupVersionResource]informer.GVRPartialMetadata{
	{
		Version:  "v1",
		Resource: "configmaps",
	}: {
		Scope: apiextensionsv1.NamespaceScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "configMap",
			Kind:     "configmap",
		},
	},
	{
		Version:  "v1",
		Resource: "secrets",
	}: {
		Scope: apiextensionsv1.NamespaceScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "secret",
			Kind:     "Secret",
		},
	},
}

func getAllGVRs(synctarget *workloadv1alpha1.SyncTarget) map[schema.GroupVersionResource]bool {
	// TODO(jmprusi): Added Configmaps and Secrets to the default syncing, but we should figure out
	//                a way to avoid doing that: https://github.com/kcp-dev/kcp/issues/727
	gvrs := map[schema.GroupVersionResource]bool{}

	for gvr := range builtinGVRs {
		gvrs[gvr] = true
	}

	// TODO(qiujian16) We currently checks the API compatibility on the server side. When we change to check the
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
