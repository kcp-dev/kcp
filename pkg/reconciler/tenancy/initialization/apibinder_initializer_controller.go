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

package initialization

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

	admission "github.com/kcp-dev/kcp/pkg/admission/clusterworkspacetypeexists"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/core/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-apibinder-initializer"
)

// NewAPIBinder returns a new controller which instantiates APIBindings and waits for them to be fully bound
// in new ClusterWorkspaces.
func NewAPIBinder(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	clusterWorkspaceTypeInformer tenancyv1alpha1informers.ClusterWorkspaceTypeClusterInformer,
	apiBindingsInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportsInformer apisv1alpha1informers.APIExportClusterInformer,
) (*APIBinder, error) {
	c := &APIBinder{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),

		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		},
		getClusterWorkspaceType: func(path logicalcluster.Path, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
			objs, err := clusterWorkspaceTypeInformer.Informer().GetIndexer().ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
			if err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, fmt.Errorf("no ClusterWorkspaceType found for %s", path.Join(name).String())
			}
			if len(objs) > 1 {
				return nil, fmt.Errorf("multiple ClusterWorkspaceTypes found for %s", path.Join(name).String())
			}
			return objs[0].(*tenancyv1alpha1.ClusterWorkspaceType), nil
		},
		listLogicalClusters: func() ([]*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().List(labels.Everything())
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
			objs, err := apiExportsInformer.Informer().GetIndexer().ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
			if err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, fmt.Errorf("no APIExport found for %s", path.Join(name).String())
			}
			if len(objs) > 1 {
				return nil, fmt.Errorf("multiple APIExports found for %s", path.Join(name).String())
			}
			return objs[0].(*apisv1alpha1.APIExport), nil
		},

		commit: committer.NewCommitter[*corev1alpha1.LogicalCluster, corev1alpha1client.LogicalClusterInterface, *corev1alpha1.LogicalClusterSpec, *corev1alpha1.LogicalClusterStatus](kcpClusterClient.CoreV1alpha1().LogicalClusters()),
	}

	c.transitiveTypeResolver = admission.NewTransitiveTypeResolver(c.getClusterWorkspaceType)

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	indexers.AddIfNotPresentOrDie(clusterWorkspaceTypeInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(obj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(obj, logger)
		},
	})

	apiBindingsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIBinding(obj, logger)

		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIBinding(obj, logger)
		},
	})

	clusterWorkspaceTypeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterWorkspaceType(obj, logger)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueClusterWorkspaceType(obj, logger)
		},
	})

	return c, nil
}

type logicalClusterResource = committer.Resource[*corev1alpha1.LogicalClusterSpec, *corev1alpha1.LogicalClusterStatus]

// APIBinder is a controller which instantiates APIBindings and waits for them to be fully bound
// in new ClusterWorkspaces.
type APIBinder struct {
	queue workqueue.RateLimitingInterface

	getLogicalCluster       func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getClusterWorkspaceType func(clusterName logicalcluster.Path, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error)
	listLogicalClusters     func() ([]*corev1alpha1.LogicalCluster, error)

	listAPIBindings  func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIBinding    func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error)
	createAPIBinding func(ctx context.Context, clusterName logicalcluster.Path, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error)

	getAPIExport func(clusterName logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	transitiveTypeResolver transitiveTypeResolver

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *logicalClusterResource) error
}

type transitiveTypeResolver interface {
	Resolve(t *tenancyv1alpha1.ClusterWorkspaceType) ([]*tenancyv1alpha1.ClusterWorkspaceType, error)
}

func (b *APIBinder) enqueueLogicalCluster(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing LogicalCluster")
	b.queue.Add(key)
}

func (b *APIBinder) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		runtime.HandleError(fmt.Errorf("expected APIBinding, got %T", obj))
		return
	}

	logger = logging.WithObject(logger, apiBinding)

	clusterName := logicalcluster.From(apiBinding)
	this, err := b.getLogicalCluster(clusterName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The workspace was deleted or is no longer initializing, so we can safely ignore this event.
			return
		}
		logger.Error(err, "failed to get LogicalCluster from lister", "cluster", clusterName)
		return // nothing we can do here
	}

	b.enqueueLogicalCluster(this, logger)
}

// enqueueClusterWorkspaceType enqueues all clusterworkspaces (which are only those that are initializing, because of
// how the informer is supposed to be configured) whenever a clusterworkspacetype changes. If a clusterworkspacetype
// had a typo in the default set of apibindings, there is a chance the requeuing here would pick up a fix.
//
// TODO(sttts): this cannot work in a sharded environment
func (b *APIBinder) enqueueClusterWorkspaceType(obj interface{}, logger logr.Logger) {
	cwt, ok := obj.(*tenancyv1alpha1.ClusterWorkspaceType)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a ClusterWorkspaceType, but is %T", obj))
		return
	}

	if len(cwt.Spec.DefaultAPIBindings) == 0 {
		return
	}

	list, err := b.listLogicalClusters()
	if err != nil {
		runtime.HandleError(fmt.Errorf("error listing clusterworkspaces: %w", err))
	}

	for _, ws := range list {
		logger := logging.WithObject(logger, ws)
		b.enqueueLogicalCluster(ws, logger)
	}
}

func (b *APIBinder) startWorker(ctx context.Context) {
	for b.processNextWorkItem(ctx) {
	}
}

func (b *APIBinder) Start(ctx context.Context, numThreads int) {
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

func (b *APIBinder) ShutDown() {
	b.queue.ShutDown()
}

func (b *APIBinder) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := b.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

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

func (b *APIBinder) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

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

	old := logicalCluster
	logicalCluster = logicalCluster.DeepCopy()

	logger = logging.WithObject(logger, logicalCluster)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	err = b.reconcile(ctx, logicalCluster)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &logicalClusterResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &logicalClusterResource{ObjectMeta: logicalCluster.ObjectMeta, Spec: &logicalCluster.Spec, Status: &logicalCluster.Status}
	if err := b.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
