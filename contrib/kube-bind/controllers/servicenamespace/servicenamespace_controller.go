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

package servicenamespace

import (
	"context"
	"fmt"
	"reflect"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	coreinformers "github.com/kcp-dev/client-go/informers/core/v1"
	rbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	corelisters "github.com/kcp-dev/client-go/listers/core/v1"
	rbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kube-bind/kube-bind/pkg/committer"
	"github.com/kube-bind/kube-bind/pkg/indexers"
	kubebindv1alpha1 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha1"
	bindclient "github.com/kube-bind/kube-bind/sdk/kcp/clientset/versioned"
	bindinformers "github.com/kube-bind/kube-bind/sdk/kcp/informers/externalversions/kubebind/v1alpha1"
	bindlisters "github.com/kube-bind/kube-bind/sdk/kcp/listers/kubebind/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	controllerName = "kube-bind-example-backend-servicenamespace"
)

// NewController returns a new controller for ServiceNamespaces.
func NewController(
	config *rest.Config,
	scope kubebindv1alpha1.Scope,
	serviceNamespaceInformer bindinformers.APIServiceNamespaceClusterInformer,
	clusterBindingInformer bindinformers.ClusterBindingClusterInformer,
	serviceExportInformer bindinformers.APIServiceExportClusterInformer,
	namespaceInformer coreinformers.NamespaceClusterInformer,
	roleInformer rbacinformers.RoleClusterInformer,
	roleBindingInformer rbacinformers.RoleBindingClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	logger := klog.Background().WithValues("Controller", controllerName)

	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, controllerName)

	bindClient, err := bindclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubernetesclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	c := &Controller{
		queue: queue,

		bindClient: bindClient,
		kubeClient: kubeClient,

		serviceNamespaceLister:  serviceNamespaceInformer.Lister(),
		serviceNamespaceIndexer: serviceNamespaceInformer.Informer().GetIndexer(),

		clusterBindingLister:  clusterBindingInformer.Lister(),
		clusterBindingIndexer: clusterBindingInformer.Informer().GetIndexer(),

		serviceExportLister:  serviceExportInformer.Lister(),
		serviceExportIndexer: serviceExportInformer.Informer().GetIndexer(),

		namespaceLister:  namespaceInformer.Lister(),
		namespaceIndexer: namespaceInformer.Informer().GetIndexer(),

		roleLister:  roleInformer.Lister(),
		roleIndexer: roleInformer.Informer().GetIndexer(),

		roleBindingLister:  roleBindingInformer.Lister(),
		roleBindingIndexer: roleBindingInformer.Informer().GetIndexer(),

		reconciler: reconciler{
			scope: scope,

			getNamespace: func(cluster logicalcluster.Name, name string) (*corev1.Namespace, error) {
				return namespaceInformer.Lister().Cluster(cluster).Get(name)
			},
			createNamespace: func(ctx context.Context, cluster logicalcluster.Path, ns *corev1.Namespace) (*corev1.Namespace, error) {
				return kubeClient.CoreV1().Cluster(cluster).Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			},
			deleteNamespace: func(ctx context.Context, cluster logicalcluster.Path, name string) error {
				return kubeClient.CoreV1().Cluster(cluster).Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
			},

			getRoleBinding: func(cluster logicalcluster.Name, ns, name string) (*rbacv1.RoleBinding, error) {
				return roleBindingInformer.Lister().Cluster(cluster).RoleBindings(ns).Get(name)
			},
			createRoleBinding: func(ctx context.Context, cluster logicalcluster.Path, crb *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
				return kubeClient.RbacV1().Cluster(cluster).RoleBindings(crb.Namespace).Create(ctx, crb, metav1.CreateOptions{})
			},
			updateRoleBinding: func(ctx context.Context, cluster logicalcluster.Path, crb *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
				return kubeClient.RbacV1().Cluster(cluster).RoleBindings(crb.Namespace).Update(ctx, crb, metav1.UpdateOptions{})
			},
		},

		commit: committer.NewCommitter[*kubebindv1alpha1.APIServiceNamespace, *kubebindv1alpha1.APIServiceNamespaceSpec, *kubebindv1alpha1.APIServiceNamespaceStatus](
			func(ns string) committer.Patcher[*kubebindv1alpha1.APIServiceNamespace] {
				return bindClient.KubeBindV1alpha1().APIServiceNamespaces(ns)
			},
		),
	}

	indexers.AddIfNotPresentOrDie(serviceNamespaceInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ServiceNamespaceByNamespace: indexers.IndexServiceNamespaceByNamespace,
	})

	namespaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueNamespace(logger, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueNamespace(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueNamespace(logger, obj)
		},
	})

	serviceNamespaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueServiceNamespace(logger, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueServiceNamespace(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueServiceNamespace(logger, obj)
		},
	})

	clusterBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterBinding(logger, obj)
		},
	})

	serviceExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			oldExport, ok := old.(*kubebindv1alpha1.APIServiceExport)
			if !ok {
				return
			}
			newExport, ok := old.(*kubebindv1alpha1.APIServiceExport)
			if !ok {
				return
			}
			if reflect.DeepEqual(oldExport.Spec, newExport.Spec) {
				return
			}
			c.enqueueServiceExport(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
	})

	return c, nil
}

type Resource = committer.Resource[*kubebindv1alpha1.APIServiceNamespaceSpec, *kubebindv1alpha1.APIServiceNamespaceStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// Controller reconciles ServiceNamespaces by creating a Namespace for each, and deleting it if
// the APIServiceNamespace is deleted.
type Controller struct {
	queue workqueue.RateLimitingInterface

	bindClient bindclient.Interface
	kubeClient kubernetesclient.ClusterInterface

	namespaceLister  corelisters.NamespaceClusterLister
	namespaceIndexer cache.Indexer

	serviceNamespaceLister  bindlisters.APIServiceNamespaceClusterLister
	serviceNamespaceIndexer cache.Indexer

	clusterBindingLister  bindlisters.ClusterBindingClusterLister
	clusterBindingIndexer cache.Indexer

	serviceExportLister  bindlisters.APIServiceExportClusterLister
	serviceExportIndexer cache.Indexer

	roleLister  rbaclisters.RoleClusterLister
	roleIndexer cache.Indexer

	roleBindingLister  rbaclisters.RoleBindingClusterLister
	roleBindingIndexer cache.Indexer

	reconciler

	commit CommitFunc
}

func (c *Controller) enqueueServiceNamespace(logger klog.Logger, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger.V(2).Info("queueing APIServiceNamespace", "key", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueClusterBinding(logger klog.Logger, obj interface{}) {
	cbKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, _, err := cache.SplitMetaNamespaceKey(cbKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	snss, err := c.serviceNamespaceIndexer.ByIndex(cache.NamespaceIndex, ns)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger.V(2).Info("queueing ServiceNamespaces", "namespace", ns, "number", len(snss), "reason", "ClusterBinding", "ClusterBindingKey", cbKey)
	for _, sns := range snss {
		key, err := cache.MetaNamespaceKeyFunc(sns)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		c.queue.Add(key)
	}
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

	snss, err := c.serviceNamespaceIndexer.ByIndex(cache.NamespaceIndex, ns)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger.V(2).Info("queueing ServiceNamespaces", "namespace", ns, "number", len(snss), "reason", "APIServiceExport", "ServiceExportKey", seKey)
	for _, sns := range snss {
		key, err := cache.MetaNamespaceKeyFunc(sns)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		c.queue.Add(key)
	}
}

func (c *Controller) enqueueNamespace(logger klog.Logger, obj interface{}) {
	nsKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	sns, err := c.serviceNamespaceIndexer.ByIndex(indexers.ServiceNamespaceByNamespace, nsKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, obj := range sns {
		key, err := cache.MetaNamespaceKeyFunc(obj)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logger.V(2).Info("queueing APIServiceNamespace", "key", key, "reason", "Namespace", "NamespaceKey", nsKey)
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx).WithValues("Controller", controllerName)

	logger.Info("Starting Controller")
	defer logger.Info("Shutting down Controller")

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
		runtime.HandleError(fmt.Errorf("%q Controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	clusterName, snsNamespace, snsName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return nil
	}
	nsName := clusterName.String() + "-" + snsNamespace + "-" + snsName

	obj, err := c.serviceNamespaceLister.Cluster(clusterName).APIServiceNamespaces(snsNamespace).Get(snsName)
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		if err := c.deleteNamespace(ctx, clusterName.Path(), nsName); err != nil && !errors.IsNotFound(err) {
			return err
		}
		return nil
	}

	old := obj
	obj = obj.DeepCopy()

	var errs []error
	if err := c.reconcile(ctx, clusterName, obj); err != nil {
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
