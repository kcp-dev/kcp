/*
Copyright 2022 The Kube Bind Authors.

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

package clusterbinding

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers/core/v1"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kubebindv1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1"
	bindclient "github.com/kube-bind/kube-bind/pkg/client/clientset/versioned"
	bindinformers "github.com/kube-bind/kube-bind/pkg/client/informers/externalversions/kubebind/v1alpha1"
	bindlisters "github.com/kube-bind/kube-bind/pkg/client/listers/kubebind/v1alpha1"
	"github.com/kube-bind/kube-bind/pkg/committer"
)

const (
	controllerName = "kube-bind-example-backend-clusterbinding"
)

// NewController returns a new controller to reconcile ClusterBindings.
func NewController(
	config *rest.Config,
	scope kubebindv1alpha1.Scope,
	clusterBindingInformer bindinformers.ClusterBindingInformer,
	serviceExportInformer bindinformers.APIServiceExportInformer,
	clusterRoleInformer rbacinformers.ClusterRoleInformer,
	clusterRoleBindingInformer rbacinformers.ClusterRoleBindingInformer,
	roleBindingInformer rbacinformers.RoleBindingInformer,
	namespaceInformer kubeinformers.NamespaceInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	logger := klog.Background().WithValues("controller", controllerName)

	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, controllerName)

	bindClient, err := bindclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubeclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	c := &Controller{
		queue: queue,

		clusterBindingLister:  clusterBindingInformer.Lister(),
		clusterBindingIndexer: clusterBindingInformer.Informer().GetIndexer(),

		serviceExportLister:  serviceExportInformer.Lister(),
		serviceExportIndexer: serviceExportInformer.Informer().GetIndexer(),

		clusterRoleLister:  clusterRoleInformer.Lister(),
		clusterRoleIndexer: clusterRoleInformer.Informer().GetIndexer(),

		clusterRoleBindingLister:  clusterRoleBindingInformer.Lister(),
		clusterRoleBindingIndexer: clusterRoleBindingInformer.Informer().GetIndexer(),

		namespaceLister:  namespaceInformer.Lister(),
		namespaceIndexer: namespaceInformer.Informer().GetIndexer(),

		reconciler: reconciler{
			scope: scope,
			listServiceExports: func(ns string) ([]*kubebindv1alpha1.APIServiceExport, error) {
				return serviceExportInformer.Lister().APIServiceExports(ns).List(labels.Everything())
			},
			getClusterRole: func(name string) (*rbacv1.ClusterRole, error) {
				return clusterRoleInformer.Lister().Get(name)
			},
			createClusterRole: func(ctx context.Context, binding *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
				return kubeClient.RbacV1().ClusterRoles().Create(ctx, binding, metav1.CreateOptions{})
			},
			updateClusterRole: func(ctx context.Context, binding *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
				return kubeClient.RbacV1().ClusterRoles().Update(ctx, binding, metav1.UpdateOptions{})
			},
			getClusterRoleBinding: func(name string) (*rbacv1.ClusterRoleBinding, error) {
				return clusterRoleBindingInformer.Lister().Get(name)
			},
			createClusterRoleBinding: func(ctx context.Context, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
				return kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
			},
			updateClusterRoleBinding: func(ctx context.Context, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
				return kubeClient.RbacV1().ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{})
			},
			deleteClusterRoleBinding: func(ctx context.Context, name string) error {
				return kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
			},
			getNamespace: func(name string) (*v1.Namespace, error) {
				return namespaceInformer.Lister().Get(name)
			},
			createRoleBinding: func(ctx context.Context, ns string, binding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
				return kubeClient.RbacV1().RoleBindings(ns).Create(ctx, binding, metav1.CreateOptions{})
			},
			updateRoleBinding: func(ctx context.Context, ns string, binding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
				return kubeClient.RbacV1().RoleBindings(ns).Update(ctx, binding, metav1.UpdateOptions{})
			},
			getRoleBinding: func(ns, name string) (*rbacv1.RoleBinding, error) {
				return roleBindingInformer.Lister().RoleBindings(ns).Get(name)
			},
		},

		commit: committer.NewCommitter[*kubebindv1alpha1.ClusterBinding, *kubebindv1alpha1.ClusterBindingSpec, *kubebindv1alpha1.ClusterBindingStatus](
			func(ns string) committer.Patcher[*kubebindv1alpha1.ClusterBinding] {
				return bindClient.KubeBindV1alpha1().ClusterBindings(ns)
			},
		),
	}

	clusterBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterBinding(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			c.enqueueClusterBinding(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterBinding(logger, obj)
		},
	})

	serviceExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			c.enqueueServiceExport(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
	})

	return c, nil
}

type Resource = committer.Resource[*kubebindv1alpha1.ClusterBindingSpec, *kubebindv1alpha1.ClusterBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// Controller reconciles ClusterBinding conditions.
type Controller struct {
	queue workqueue.RateLimitingInterface

	clusterBindingLister  bindlisters.ClusterBindingLister
	clusterBindingIndexer cache.Indexer

	serviceExportLister  bindlisters.APIServiceExportLister
	serviceExportIndexer cache.Indexer

	clusterRoleLister  rbaclisters.ClusterRoleLister
	clusterRoleIndexer cache.Indexer

	clusterRoleBindingLister  rbaclisters.ClusterRoleBindingLister
	clusterRoleBindingIndexer cache.Indexer

	namespaceLister  corelisters.NamespaceLister
	namespaceIndexer cache.Indexer

	reconciler

	commit CommitFunc
}

func (c *Controller) enqueueClusterBinding(logger klog.Logger, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger.V(2).Info("queueing ClusterBinding", "key", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueServiceExport(logger klog.Logger, obj interface{}) {
	seKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, _, err := cache.SplitMetaNamespaceKey(seKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	key := ns + "/cluster"
	logger.V(2).Info("queueing ClusterBinding", "key", key, "reason", "APIServiceExport", "ServiceExportKey", seKey)
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx).WithValues("controller", controllerName)

	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	defer runtime.HandleCrash()

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

	logger := klog.FromContext(ctx).WithValues("key", key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil // we cannot do anything
	}

	obj, err := c.clusterBindingLister.ClusterBindings(ns).Get(name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		logger.V(2).Info("ClusterBinding not found, ignoring")
		return nil // nothing we can do
	}

	old := obj
	obj = obj.DeepCopy()

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
