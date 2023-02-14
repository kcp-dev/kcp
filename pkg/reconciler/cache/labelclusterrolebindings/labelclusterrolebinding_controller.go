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

package labelclusterrolebindings

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcprbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	rbacclientv1 "k8s.io/client-go/kubernetes/typed/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterroles"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

type Controller interface {
	Start(ctx context.Context, numThreads int)

	EnqueueClusterRoleBindings(values ...interface{})
}

// NewController returns a new controller for labelling ClusterRoleBinding that should be replicated.
func NewController(
	controllerName string,
	groupName string,
	isRelevantClusterRole func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool,
	isRelevantClusterRoleBinding func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
) Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		controllerName: controllerName,
		groupName:      groupName,

		queue: queue,

		isRelevantClusterRole:        isRelevantClusterRole,
		isRelevantClusterRoleBinding: isRelevantClusterRoleBinding,

		kubeClusterClient: kubeClusterClient,

		clusterRoleBindingLister:  clusterRoleBindingInformer.Lister(),
		clusterRoleBindingIndexer: clusterRoleBindingInformer.Informer().GetIndexer(),

		clusterRoleLister:  clusterRoleInformer.Lister(),
		clusterRoleIndexer: clusterRoleInformer.Informer().GetIndexer(),

		commit: committer.NewStatuslessCommitter[*rbacv1.ClusterRoleBinding, rbacclientv1.ClusterRoleBindingInterface](kubeClusterClient.RbacV1().ClusterRoleBindings(), committer.ShallowCopy[rbacv1.ClusterRoleBinding]),
	}

	indexers.AddIfNotPresentOrDie(clusterRoleBindingInformer.Informer().GetIndexer(), cache.Indexers{
		labelclusterroles.ClusterRoleBindingByClusterRoleName: labelclusterroles.IndexClusterRoleBindingByClusterRoleName,
	})

	clusterRoleBindingInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueClusterRoleBinding(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueClusterRoleBinding(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueClusterRoleBinding(obj)
			},
		},
	})

	clusterRoleInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueClusterRole(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueClusterRole(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueClusterRole(obj)
			},
		},
	})

	return c
}

// controller reconciles ClusterRoleBindings by labelling them to be replicated when
// 1. either a maximum-permission-policy subject is bound
// 2. or a ClusterRole.
type controller struct {
	controllerName string
	groupName      string

	queue workqueue.RateLimitingInterface

	isRelevantClusterRole        func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool
	isRelevantClusterRoleBinding func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	clusterRoleBindingLister  kcprbaclisters.ClusterRoleBindingClusterLister
	clusterRoleBindingIndexer cache.Indexer

	clusterRoleLister  kcprbaclisters.ClusterRoleClusterLister
	clusterRoleIndexer cache.Indexer

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *rbacv1.ClusterRoleBinding) error
}

func (c *controller) EnqueueClusterRoleBindings(values ...interface{}) {
	clusterRoleBindings, err := c.clusterRoleBindingLister.List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, clusterRoleBinding := range clusterRoleBindings {
		c.enqueueClusterRoleBinding(clusterRoleBinding, values...)
	}
}

// enqueueClusterRoleBinding enqueues an ClusterRoleBinding.
func (c *controller) enqueueClusterRoleBinding(obj interface{}, values ...interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.controllerName), key)
	logger.V(4).WithValues(values...).Info("queueing ClusterRoleBinding")
	c.queue.Add(key)
}

func (c *controller) enqueueClusterRole(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	_, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	objs, err := c.clusterRoleBindingIndexer.ByIndex(labelclusterroles.ClusterRoleBindingByClusterRoleName, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, obj := range objs {
		crb := obj.(*rbacv1.ClusterRoleBinding)
		c.enqueueClusterRoleBinding(crb, "reason", "ClusterRole", "ClusterRole.name", name)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.controllerName)
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
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", c.controllerName, key, err))
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
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false, nil
	}

	crb, err := c.clusterRoleBindingLister.Cluster(cluster).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	logger := logging.WithObject(klog.FromContext(ctx), crb)
	ctx = klog.NewContext(ctx, logger)

	old := crb
	crb = crb.DeepCopy()

	var errs []error
	requeue, err := c.reconcile(ctx, crb)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	if err := c.commit(ctx, old, crb); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
