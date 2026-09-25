/*
Copyright 2022 The kcp Authors.

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

package permissionclaimlabel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/apis/v1alpha2"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-permissionclaimlabel"

	// informerRetryDelay is how long a binding waits before it is retried
	// when a claimed resource has no synced dynamic informer yet.
	informerRetryDelay = 2 * time.Second
)

// errInformerNotReady is wrapped by getInformerForGroupResource when no synced
// dynamic informer exists for a claimed resource. That is normal right after
// the resource is first bound on a shard: the informer is created from
// discovery and needs a moment to list.
var errInformerNotReady = errors.New("unable to find informer")

// informerNotReadyError is returned by reconcile when every failure was a
// missing informer. It tells the queue to wait on a fixed delay instead of
// counting a failure: a burst of unrelated updates to the binding (status
// patches, claim labels) otherwise stacks up per-item exponential backoff
// within a second and parks the binding for up to the 1000s cap - long after
// the informer has synced - until something else happens to touch it.
type informerNotReadyError struct {
	err error
}

func (e *informerNotReadyError) Error() string { return e.err.Error() }
func (e *informerNotReadyError) Unwrap() error { return e.err }

// onlyInformerNotReady reports whether errs is non-empty and every entry is
// a missing-informer error.
func onlyInformerNotReady(errs []error) bool {
	if len(errs) == 0 {
		return false
	}
	for _, err := range errs {
		if !errors.Is(err, errInformerNotReady) {
			return false
		}
	}
	return true
}

// waitingForInformer reports whether err, as returned by process, consists
// solely of reconcile waiting for dynamic informers - i.e. no real failure
// (including the status commit) is mixed in.
func waitingForInformer(err error) bool {
	if err == nil {
		return false
	}
	if agg, ok := err.(utilerrors.Aggregate); ok {
		errs := agg.Errors()
		if len(errs) == 0 {
			return false
		}
		for _, e := range errs {
			if !waitingForInformer(e) {
				return false
			}
		}
		return true
	}
	var notReady *informerNotReadyError
	return errors.As(err, &notReady)
}

// NewController returns a new controller for handling permission claims for an APIBinding.
// it will own the AppliedPermissionClaims and will own the accepted permission claim condition.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) (*controller, error) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)

	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		kcpClusterClient:     kcpClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		ddsif:                dynamicDiscoverySharedInformerFactory,

		apiBindingsLister:  apiBindingInformer.Lister(),
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),

		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},

		commit: committer.NewCommitter[*APIBinding, Patcher, *APIBindingSpec, *APIBindingStatus](kcpClusterClient.ApisV1alpha2().APIBindings()),
	}

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIBinding(newObj, logger)
		},
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
	})

	// Claim labels embed the claimed identity hash, normalized to the
	// export's canonical identity. When an export's identity is rotated or
	// an alias is registered/retired, every binding claiming its resources
	// must be re-reconciled so claimed objects are relabeled with the newly
	// canonical hash.
	exportIdentityHandler := cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldExport, ok := oldObj.(*apisv1alpha2.APIExport)
			if !ok {
				return
			}
			newExport, ok := newObj.(*apisv1alpha2.APIExport)
			if !ok {
				return
			}
			if oldExport.Status.IdentityHash == newExport.Status.IdentityHash &&
				slices.Equal(oldExport.Status.IdentityAliasHashes, newExport.Status.IdentityAliasHashes) {
				return
			}
			for _, resource := range newExport.Spec.Resources {
				c.enqueueByGroupResource(schema.GroupResource{Group: resource.Group, Resource: resource.Name}, logger)
			}
		},
	}
	_, _ = apiExportInformer.Informer().AddEventHandler(exportIdentityHandler)
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(exportIdentityHandler)

	return c, nil
}

type APIBinding = apisv1alpha2.APIBinding
type APIBindingSpec = apisv1alpha2.APIBindingSpec
type APIBindingStatus = apisv1alpha2.APIBindingStatus
type Patcher = apisv1alpha2client.APIBindingInterface
type Resource = committer.Resource[*APIBindingSpec, *APIBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles resource labels that make claimed resources visible to an APIExport
// owner. It labels resources in the intersection of `APIBinding.status.permissionClaims` and
// `APIBinding.spec.acceptedPermissionClaims`.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	kcpClusterClient     kcpclientset.ClusterInterface
	apiBindingsIndexer   cache.Indexer
	dynamicClusterClient kcpdynamic.ClusterInterface
	ddsif                *informer.DiscoveringDynamicSharedInformerFactory

	apiBindingsLister apisv1alpha2listers.APIBindingClusterLister
	getAPIExport      func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)

	commit CommitFunc
}

// enqueueAPIBinding enqueues an APIBinding.
func (c *controller) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info("queueing APIBinding")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("starting controller")
	defer logger.Info("shutting down controller")

	// React to GVRs being added or removed by the dynamic informer factory and
	// only enqueue bindings whose accepted permission claims reference that
	// group/resource. This unblocks bindings whose claim was waiting on an
	// informer that just appeared (e.g. a bound CRD landing on this shard) and
	// re-runs them when a backing informer goes away.
	c.ddsif.AddGVRLifecycleHandler(ctx, informer.GVRLifecycleHandlerFuncs{
		AddedFunc: func(gvr schema.GroupVersionResource) {
			// Wait for the informer to sync before enqueueing APIBindings that reference it.
			// This ensures that the informer is ready to list/watch the resources when
			// the APIBinding is reconciled.
			go func() {
				if waitForInformerSynced(ctx, gvr, c.ddsif.Informers) {
					c.enqueueByGroupResource(gvr.GroupResource(), logger)
				}
			}()
		},
		RemovedFunc: func(gvr schema.GroupVersionResource) { c.enqueueByGroupResource(gvr.GroupResource(), logger) },
	})

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// enqueueByGroupResource enqueues every APIBinding that has an accepted permission
// claim for the given group/resource, across all clusters on this shard.
func (c *controller) enqueueByGroupResource(gr schema.GroupResource, logger logr.Logger) {
	bindings, err := indexers.ListAPIBindingsByAcceptedClaimedGroupResource(c.apiBindingsIndexer, gr)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to list APIBindings by claimed group resource %q: %w", gr, err))
		return
	}
	if len(bindings) == 0 {
		return
	}
	logger.V(4).Info("re-enqueueing APIBindings claiming changed GVR", "groupResource", gr.String(), "count", len(bindings))
	for _, b := range bindings {
		key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(b)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}
		c.queue.Add(key)
	}
}

// waitForInformerSynced blocks until informers reports gvr as synced, and returns
// true. It returns false if gvr disappears from informers first (the informer was
// removed before syncing) or ctx is done.
func waitForInformerSynced[T any](ctx context.Context, gvr schema.GroupVersionResource, informers func() (map[schema.GroupVersionResource]T, []schema.GroupVersionResource)) bool {
	var synced bool
	err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(context.Context) (bool, error) {
		syncedInformers, notSynced := informers()
		if _, synced = syncedInformers[gvr]; synced {
			return true, nil
		}
		return !slices.Contains(notSynced, gvr), nil
	})
	return err == nil && synced
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		if waitingForInformer(err) {
			logger.V(2).Info("waiting for the dynamic informer of a claimed resource", "reason", err.Error(), "retryIn", informerRetryDelay)
			c.queue.Forget(key)
			c.queue.AddAfter(key, informerRetryDelay)
			return true
		}
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.apiBindingsLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(apiExportInformer apisv1alpha2informers.APIExportClusterInformer, apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer) {
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	if err := apiBindingInformer.Informer().GetIndexer().AddIndexers(
		cache.Indexers{
			indexers.APIBindingByClusterAndAcceptedClaimedGroupResources: indexers.IndexAPIBindingByClusterAndAcceptedClaimedGroupResources,
			indexers.APIBindingByAcceptedClaimedGroupResource:            indexers.IndexAPIBindingByAcceptedClaimedGroupResource,
		},
	); err != nil {
		panic(err)
	}
}
