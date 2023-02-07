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
	workloadv1alpha1typed "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/workload/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "kcp-syncer-synctarget-gvrsource-controller"
)

// NewSyncTargetGVRSource returns a controller watching a [workloadv1alpha1.SyncTarget] and maintaining,
// from the information contained in the SyncTarget status,
// a list of the GVRs that should be watched and synced.
//
// It implements the [informer.GVRSource] interface to provide the GVRs to sync, as well as
// a way to subscribe to changes in the GVR list.
// It will be used to feed the various [informer.DiscoveringDynamicSharedInformerFactory] instances
// (one for downstream and 2 for upstream, for syncing and upsyncing).
func NewSyncTargetGVRSource(
	syncerLogger logr.Logger,
	downstreamSyncerDiscoveryClient discovery.DiscoveryInterface,
	upstreamDynamicClusterClient kcpdynamic.ClusterInterface,
	downstreamDynamicClient dynamic.Interface,
	downstreamKubeClient kubernetes.Interface,
	syncTargetClient workloadv1alpha1typed.SyncTargetInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	syncTargetName string,
	syncTargetClusterName logicalcluster.Name,
	syncTargetUID types.UID,
) (*controller, error) {
	c := &controller{
		queue:                           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		downstreamSyncerDiscoveryClient: memory.NewMemCacheClient(downstreamSyncerDiscoveryClient),
		downstreamKubeClient:            downstreamKubeClient,
		syncTargetClient:                syncTargetClient,
		syncTargetUID:                   syncTargetUID,
		syncTargetLister:                syncTargetInformer.Lister(),
		synctargetInformerHasSynced:     syncTargetInformer.Informer().HasSynced,
		gvrsToWatch:                     map[schema.GroupVersionResource]informer.GVRPartialMetadata{},
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

var _ informer.GVRSource = (*controller)(nil)

// controller is a control loop that watches synctarget. It starts/stops spec syncer and status syncer
// per gvr based on synctarget.Status.SyncedResources.
// All the spec/status syncer share the same downstreamNSInformer and upstreamSecretInformer. Informers
// for gvr is started separated for each syncer.
type controller struct {
	queue                workqueue.RateLimitingInterface
	downstreamKubeClient kubernetes.Interface

	downstreamSyncerDiscoveryClient discovery.CachedDiscoveryInterface

	syncTargetUID               types.UID
	syncTargetLister            workloadv1alpha1listers.SyncTargetLister
	synctargetInformerHasSynced cache.InformerSynced
	syncTargetClient            workloadv1alpha1typed.SyncTargetInterface

	gvrsToWatchLock sync.RWMutex
	gvrsToWatch     map[schema.GroupVersionResource]informer.GVRPartialMetadata

	// Support subscribers that want to know when Synced GVRs have changed.
	subscribersLock sync.Mutex
	subscribers     []chan<- struct{}
}

// GVRs returns the required metadata (scope, kind, singular name) about all GVRs that should be synced.
// It implements [informer.GVRSource.GVRs].
func (c *controller) GVRs() map[schema.GroupVersionResource]informer.GVRPartialMetadata {
	c.gvrsToWatchLock.RLock()
	defer c.gvrsToWatchLock.RUnlock()

	gvrs := make(map[schema.GroupVersionResource]informer.GVRPartialMetadata, len(c.gvrsToWatch)+len(builtinGVRs)+1)
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
	for key, value := range c.gvrsToWatch {
		gvrs[key] = value
	}
	return gvrs
}

// Ready returns true if the controller is ready to return the GVRs to sync.
// It implements [informer.GVRSource.Ready].
func (c *controller) Ready() bool {
	return c.synctargetInformerHasSynced()
}

// Subscribe returns a new channel to which the controller writes whenever
// its list of GVRs has changed.
// It implements [informer.GVRSource.Subscribe].
func (c *controller) Subscribe() <-chan struct{} {
	c.subscribersLock.Lock()
	defer c.subscribersLock.Unlock()

	// Use a buffered channel so we can always send at least 1, regardless of consumer status.
	changes := make(chan struct{}, 1)
	c.subscribers = append(c.subscribers, changes)

	return changes
}

func (c *controller) enqueueSyncTarget(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing SyncTarget")

	c.queue.Add(key)
}

// Start starts the controller workers.
func (c *controller) Start(ctx context.Context, numThreads int) {
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

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}

	syncTarget, err := c.syncTargetLister.Get(name)
	if apierrors.IsNotFound(err) {
		if updated := c.removeUnusedGVRs(ctx, map[schema.GroupVersionResource]bool{}); updated {
			c.notifySubscribers(ctx)
		}
		return nil
	}

	if err != nil {
		return err
	}

	if syncTarget.GetUID() != c.syncTargetUID {
		return nil
	}

	requiredGVRs := getAllGVRs(syncTarget)

	c.downstreamSyncerDiscoveryClient.Invalidate()

	var errs []error
	var unauthorizedGVRs []string
	notify := false
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

		if updated, err := c.addGVR(ctx, gvr); err != nil {
			return err
		} else if updated {
			notify = true
		}
	}

	if updated := c.removeUnusedGVRs(ctx, requiredGVRs); updated {
		notify = true
	}

	if notify {
		c.notifySubscribers(ctx)
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

func (c *controller) patchSyncTargetCondition(ctx context.Context, new, old *workloadv1alpha1.SyncTarget) error {
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
	_, uerr := c.syncTargetClient.Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return uerr
}

func (c *controller) checkSSAR(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
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

// removeUnusedGVRs removes the GVRs which are not required anymore, and return `true` if GVRs were updated.
func (c *controller) removeUnusedGVRs(ctx context.Context, requiredGVRs map[schema.GroupVersionResource]bool) bool {
	logger := klog.FromContext(ctx)

	c.gvrsToWatchLock.Lock()
	defer c.gvrsToWatchLock.Unlock()

	updated := false
	for gvr := range c.gvrsToWatch {
		if _, ok := requiredGVRs[gvr]; !ok {
			logger.WithValues("gvr", gvr.String()).V(2).Info("Stop syncer for gvr")
			delete(c.gvrsToWatch, gvr)
			updated = true
		}
	}
	return updated
}

// addGVR adds the given GVR if it isn't already in the list, and returns `true` if the GVR was added,
// `false` if it was already there.
func (c *controller) addGVR(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	logger := klog.FromContext(ctx)

	c.gvrsToWatchLock.Lock()
	defer c.gvrsToWatchLock.Unlock()

	if _, ok := c.gvrsToWatch[gvr]; ok {
		logger.V(2).Info("Informer is started already")
		return false, nil
	}

	partialMetadata, err := c.getGVRPartialMetadata(gvr)
	if err != nil {
		return false, err
	}

	c.gvrsToWatch[gvr] = *partialMetadata

	return true, nil
}

func (c *controller) getGVRPartialMetadata(gvr schema.GroupVersionResource) (*informer.GVRPartialMetadata, error) {
	apiResourceList, err := c.downstreamSyncerDiscoveryClient.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
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

func (c *controller) notifySubscribers(ctx context.Context) {
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
