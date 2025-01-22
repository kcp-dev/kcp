/*
Copyright 2025 The KCP Authors.

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

// TODO(blut): consider where this controller should be located (tenancy, core, ...)

package defaultapibindinglifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	admission "github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
)

const (
	ControllerName = "kcp-default-apibinding-controller"
)

// NewDefaultAPIBindingController returns a new controller which instantiates APIBindings and waits for them to be fully bound
// in new Workspaces.
func NewDefaultAPIBindingController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	workspaceInformer, globalWorkspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	workspaceTypeInformer, globalWorkspaceTypeInformer tenancyv1alpha1informers.WorkspaceTypeClusterInformer,
	apiBindingsInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportsInformer, globalAPIExportsInformer apisv1alpha1informers.APIExportClusterInformer,
) (*DefaultAPIBindingController, error) {
	c := &DefaultAPIBindingController{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),

		// TODO(blut): clean up which of these fuctions are actually needed
		// TODO(blut): consider splitting the work of binding APIs / cleanup through different resources

		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		},

		listLogicalClusters: func() ([]*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().List(labels.Everything())
		},

		getWorkspaceType: func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
			return indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), workspaceTypeInformer.Informer().GetIndexer(), globalWorkspaceTypeInformer.Informer().GetIndexer(), path, name)
		},

		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			return apiBindingsInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error) {
			return apiBindingsInformer.Lister().Cluster(clusterName).Get(name)
		},
		createAPIBinding: func(ctx context.Context, clusterName logicalcluster.Path, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error) {
			return kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		},

		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), apiExportsInformer.Informer().GetIndexer(), globalAPIExportsInformer.Informer().GetIndexer(), path, name)
		},
	}

	c.transitiveTypeResolver = admission.NewTransitiveTypeResolver(c.getWorkspaceType)

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	// needed to reconcile if users change / delete their maintained APIBindings
	_, _ = apiBindingsInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj, logger) },
		// at the moment we don't have APIBinding protection
		// to recreate if manually deleted seems fair, as they are also orphaned after removal from the WorkspaceType
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
	}))

	// needed to reconcile if users change their workspace lifecycle mode
	_, _ = workspaceInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspace(obj, logger) },
	}))
	_, _ = globalWorkspaceInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspace(obj, logger) },
	}))

	// needed to reconcile if providers change defaultAPIBindings on their WorkspaceTypes
	_, _ = workspaceTypeInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceTypes(obj, logger) },
	}))
	_, _ = globalWorkspaceTypeInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceTypes(obj, logger) },
	}))

	// needed to reconcile when new published resources or claims are added to api exports
	_, _ = apiExportsInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger) },
	}))
	_, _ = globalAPIExportsInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger) },
	}))

	return c, nil
}

// DefaultAPIBindingController is a controller which instantiates APIBindings and waits for them to be fully bound
// in new Workspaces.
type DefaultAPIBindingController struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getLogicalCluster   func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getWorkspaceType    func(clusterName logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)
	listLogicalClusters func() ([]*corev1alpha1.LogicalCluster, error)

	listAPIBindings  func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIBinding    func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error)
	createAPIBinding func(ctx context.Context, clusterName logicalcluster.Path, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error)

	getAPIExport func(clusterName logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	transitiveTypeResolver transitiveTypeResolver
}

type transitiveTypeResolver interface {
	Resolve(t *tenancyv1alpha1.WorkspaceType) ([]*tenancyv1alpha1.WorkspaceType, error)
}

// TODO(blut): remove "FEAT " from logging
func (c *DefaultAPIBindingController) enqueueLogicalCluster(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

func (c *DefaultAPIBindingController) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		runtime.HandleError(fmt.Errorf("expected APIBinding, got %T", obj))
		return
	}

	logger = logging.WithObject(logger, apiBinding)

	clusterName := logicalcluster.From(apiBinding)

	logicalCluster, err := c.getLogicalCluster(clusterName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		logger.Error(err, "failed to get LogicalCluster from lister", "cluster", clusterName)
		return // nothing we can do here
	}

	c.enqueueLogicalCluster(logicalCluster, logger)
}

func (c *DefaultAPIBindingController) enqueueWorkspaceTypes(obj interface{}, logger logr.Logger) {
	wt, ok := obj.(*tenancyv1alpha1.WorkspaceType)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a WorkspaceType, but is %T", obj))
		return
	}

	if len(wt.Spec.DefaultAPIBindings) == 0 {
		return
	}

	list, err := c.listLogicalClusters()
	if err != nil {
		runtime.HandleError(fmt.Errorf("error listing workspaces: %w", err))
	}

	for _, ws := range list {
		logger := logging.WithObject(logger, ws)
		c.enqueueLogicalCluster(ws, logger)
	}
}

func (c *DefaultAPIBindingController) enqueueAPIExport(obj interface{}, logger logr.Logger) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be an APIExport, but is %T", obj))
		return
	}

	if len(apiExport.Spec.LatestResourceSchemas) == 0 && len(apiExport.Spec.PermissionClaims) == 0 {
		return
	}

	list, err := c.listLogicalClusters()
	if err != nil {
		runtime.HandleError(fmt.Errorf("error listing logical clusters: %w", err))
	}

	for _, ws := range list {
		logger := logging.WithObject(logger, ws)
		c.enqueueLogicalCluster(ws, logger)
	}
}

func (c *DefaultAPIBindingController) enqueueWorkspace(obj interface{}, logger logr.Logger) {
	ws, ok := obj.(*tenancyv1alpha1.Workspace)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a Workspace, but is %T", obj))
		return
	}

	logger = logging.WithObject(logger, ws)

	logicalCluster, err := c.getLogicalCluster(logicalcluster.Name(ws.Spec.Cluster))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		logger.Error(err, "failed to get LogicalCluster from lister", "cluster", ws.Spec.Cluster)
		return // nothing we can do here
	}

	c.enqueueLogicalCluster(logicalCluster, logger)
}

func (b *DefaultAPIBindingController) startWorker(ctx context.Context) {
	for b.processNextWorkItem(ctx) {
	}
}

func (b *DefaultAPIBindingController) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer b.queue.ShutDown()
	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)

	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, b.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (b *DefaultAPIBindingController) ShutDown() {
	b.queue.ShutDown()
}

func (b *DefaultAPIBindingController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := b.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer b.queue.Done(key)

	if err := b.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", ControllerName, key, err))
		b.queue.AddRateLimited(key)
		return true
	}

	b.queue.Forget(key)
	return true
}

func (b *DefaultAPIBindingController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	klog.V(3).Infof("FEAT processing item %s", key)

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "unable to decode key")
		return nil
	}

	logicalCluster, err := b.getLogicalCluster(clusterName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get LogicalCluster from lister", "cluster", clusterName)
		}

		return nil // nothing we can do here
	}

	logicalCluster = logicalCluster.DeepCopy()

	logger = logging.WithObject(logger, logicalCluster)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	err = b.reconcile(ctx, logicalCluster)
	if err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// TODO(blut): validate sharding selection (no idea how this works...)
// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer, globalApiExportInformer apisv1alpha1informers.APIExportClusterInformer) {
	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(globalApiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
