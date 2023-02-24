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

package apilifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apis/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-apilifecycle"
)

// NewController returns a new controller for APILifecycles.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	config *rest.Config,
	apiLifecycleInformer apisv1alpha1informers.APILifecycleClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha1informers.APIExportClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	apiLifecycleInformer.Informer()

	c := &controller{
		queue:  queue,
		config: config,

		apiBindingLister: apiBindingInformer.Lister(),

		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			// Try local informer first
			export, err := indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), path, name)
			if err == nil {
				// Quick happy path - found it locally
				return export, nil
			}
			if !apierrors.IsNotFound(err) {
				// Unrecoverable error
				return nil, err
			}
			// Didn't find it locally - try remote
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},

		getAPILifecycleByAPIExport: func(apiExport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APILifecycle, error) {
			_, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiExport)
			if err != nil {
				return nil, err
			}

			cluster := logicalcluster.From(apiExport)

			objs, err := apiLifecycleInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}

			var lifecycles []*apisv1alpha1.APILifecycle
			for _, obj := range objs {
				if logicalcluster.From(obj) == cluster && obj.Spec.Reference == apiExport.Name {
					lifecycles = append(lifecycles, obj)
				}
			}

			return lifecycles, nil
		},

		commit: committer.NewCommitter[*APIBinding, Patcher, *APIBindingSpec, *APIBindingStatus](kcpClusterClient.ApisV1alpha1().APIBindings()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	// APIBinding handlers
	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](obj), logger)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](obj), logger)
		},
	})

	return c, nil
}

func objOrTombstone[T runtime.Object](obj any) T {
	if t, ok := obj.(T); ok {
		return t
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if t, ok := tombstone.Obj.(T); ok {
			return t
		}

		panic(fmt.Errorf("tombstone %T is not a %T", tombstone, new(T)))
	}

	panic(fmt.Errorf("%T is not a %T", obj, new(T)))
}

type APIBinding = apisv1alpha1.APIBinding
type APIBindingSpec = apisv1alpha1.APIBindingSpec
type APIBindingStatus = apisv1alpha1.APIBindingStatus
type Patcher = apisv1alpha1client.APIBindingInterface
type Resource = committer.Resource[*APIBindingSpec, *APIBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIBinding lifecycle hooks.
type controller struct {
	queue  workqueue.RateLimitingInterface
	commit CommitFunc
	config *rest.Config

	apiBindingLister apisv1alpha1listers.APIBindingClusterLister
	apiExportLister  apisv1alpha1listers.APIExportClusterLister

	getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	getAPILifecycleByAPIExport func(apiExport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APILifecycle, error)
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(apiBinding *apisv1alpha1.APIBinding, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiBinding)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info(fmt.Sprintf("queueing APIBinding"))
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
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

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
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

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}

	binding, err := c.apiBindingLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get APILifecycle from lister", "cluster", clusterName)
		}

		return false, nil // nothing we can do here
	}

	old := binding
	binding = binding.DeepCopy()

	logger = logging.WithObject(logger, binding)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, binding)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: binding.ObjectMeta, Spec: &binding.Spec, Status: &binding.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
