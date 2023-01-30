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

package labelclusterroles

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
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

type Controller interface {
	Start(ctx context.Context, numThreads int)

	EnqueueClusterRoles(values ...interface{})
}

// NewController returns a new controller for labelling ClusterRole that should be replicated.
func NewController(
	controllerName string,
	groupName string,
	isRelevantClusterRole func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool,
	isRelevantClusterRoleBinding func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		controllerName: controllerName,
		groupName:      groupName,

		isRelevantClusterRole:        isRelevantClusterRole,
		isRelevantClusterRoleBinding: isRelevantClusterRoleBinding,

		queue: queue,

		kubeClusterClient: kubeClusterClient,

		clusterRoleLister:  clusterRoleInformer.Lister(),
		clusterRoleIndexer: clusterRoleInformer.Informer().GetIndexer(),

		clusterRoleBindingLister:  clusterRoleBindingInformer.Lister(),
		clusterRoleBindingIndexer: clusterRoleBindingInformer.Informer().GetIndexer(),

		commit: committer.NewStatuslessCommitter[*rbacv1.ClusterRole, rbacclientv1.ClusterRoleInterface](kubeClusterClient.RbacV1().ClusterRoles(), committer.ShallowCopy[rbacv1.ClusterRole]),
	}

	indexers.AddIfNotPresentOrDie(clusterRoleBindingInformer.Informer().GetIndexer(), cache.Indexers{
		ClusterRoleBindingByClusterRoleName: IndexClusterRoleBindingByClusterRoleName,
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

	return c
}

// controller reconciles ClusterRoles by labelling them to be replicated when pointing to an
// ClusterRole content or verb bind.
type controller struct {
	controllerName string
	groupName      string

	isRelevantClusterRole        func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool
	isRelevantClusterRoleBinding func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool

	queue workqueue.RateLimitingInterface

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	clusterRoleLister  kcprbaclisters.ClusterRoleClusterLister
	clusterRoleIndexer cache.Indexer

	clusterRoleBindingLister  kcprbaclisters.ClusterRoleBindingClusterLister
	clusterRoleBindingIndexer cache.Indexer

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *rbacv1.ClusterRole) error
}

func (c *controller) EnqueueClusterRoles(values ...interface{}) {
	clusterRoles, err := c.clusterRoleLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("Error listing ClusterRoles: %v", err)
		return
	}
	for _, cr := range clusterRoles {
		c.enqueueClusterRole(cr, values...)
	}
}

// enqueueClusterRole enqueues an ClusterRole.
func (c *controller) enqueueClusterRole(obj interface{}, values ...interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.controllerName), key)
	logger.V(4).WithValues(values...).Info("queueing ClusterRole")
	c.queue.Add(key)
}

func (c *controller) enqueueClusterRoleBinding(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}

	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		runtime.HandleError(fmt.Errorf("unexpected type %T", obj))
		return
	}

	if crb.RoleRef.Kind != "ClusterRole" || crb.RoleRef.APIGroup != rbacv1.GroupName {
		return
	}

	cr, err := c.clusterRoleLister.Cluster(logicalcluster.From(crb)).Get(crb.RoleRef.Name)
	if err != nil && !errors.IsNotFound(err) {
		runtime.HandleError(err)
		return
	} else if errors.IsNotFound(err) {
		return // dangling ClusterRole reference, nothing to do
	}

	c.enqueueClusterRole(cr, "reason", "ClusterRoleBinding", "ClusterRoleBinding.name", crb.Name)
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
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false, nil
	}
	cr, err := c.clusterRoleLister.Cluster(parent).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := cr
	cr = cr.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), cr)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, cr)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	if err := c.commit(ctx, old, cr); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
