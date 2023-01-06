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

package replicationclusterrole

import (
	"context"
	"fmt"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcprbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-apiexport-replication-clusterrole"

	// ReplicateLabelKey is the label key used to indicate that a ClusterRole should be replicated.
	ReplicateLabelKey = "apis.kcp.io/replicate"
)

// NewController returns a new controller for labelling ClusterRole that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,

		kubeClusterClient: kubeClusterClient,

		clusterRoleLister:  clusterRoleInformer.Lister(),
		clusterRoleIndexer: clusterRoleInformer.Informer().GetIndexer(),

		clusterRoleBindingLister:  clusterRoleBindingInformer.Lister(),
		clusterRoleBindingIndexer: clusterRoleBindingInformer.Informer().GetIndexer(),
	}

	indexers.AddIfNotPresentOrDie(clusterRoleBindingInformer.Informer().GetIndexer(), cache.Indexers{
		ClusterRoleBindingByClusterRoleName: IndexClusterRoleBindingByClusterRoleName,
	})

	clusterRoleInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterRole(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueClusterRole(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterRole(obj)
		},
	})

	clusterRoleBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterRoleBinding(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueClusterRoleBinding(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterRoleBinding(obj)
		},
	})

	return c, nil
}

// controller reconciles ClusterRoles by labelling them to be replicated when pointing to an
// ClusterRole content or verb bind.
type controller struct {
	queue workqueue.RateLimitingInterface

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	clusterRoleLister  kcprbaclisters.ClusterRoleClusterLister
	clusterRoleIndexer cache.Indexer

	clusterRoleBindingLister  kcprbaclisters.ClusterRoleBindingClusterLister
	clusterRoleBindingIndexer cache.Indexer
}

// enqueueClusterRole enqueues an ClusterRole.
func (c *controller) enqueueClusterRole(obj interface{}, values ...interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
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
	if err != nil {
		runtime.HandleError(err)
		return
	}

	c.enqueueClusterRole(cr, "reason", "ClusterRoleBinding", "ClusterRoleBinding.name", crb.Name)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
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
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	cr, err := c.clusterRoleLister.Cluster(cluster).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	logger := logging.WithObject(klog.FromContext(ctx), cr)
	ctx = klog.NewContext(ctx, logger)

	replicate := HasBindOrContentRule(cr)
	if !replicate {
		objs, err := c.clusterRoleBindingIndexer.ByIndex(ClusterRoleBindingByClusterRoleName, key)
		if err != nil {
			runtime.HandleError(err)
			return nil // nothing we can do
		}
		for _, obj := range objs {
			crb := obj.(*rbacv1.ClusterRoleBinding)
			if HasMaximalPermissionClaimSubject(crb) {
				replicate = true
				break
			}
		}
	}

	_, found := cr.Labels[ReplicateLabelKey]
	if replicate == found {
		return nil
	}

	patch := fmt.Sprintf(`{"metadata":{"labels":{"%s":null}}}`, ReplicateLabelKey)
	if replicate {
		patch = fmt.Sprintf(`{"metadata":{"labels":{"%s":"%t"}}}`, ReplicateLabelKey, replicate)
	}

	_, err = c.kubeClusterClient.Cluster(cluster.Path()).RbacV1().ClusterRoles().Patch(ctx, cr.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

func HasBindOrContentRule(cr *rbacv1.ClusterRole) bool {
	for _, rule := range cr.Rules {
		if !sets.NewString(rule.APIGroups...).Has(apis.GroupName) {
			continue
		}
		if sets.NewString(rule.Resources...).Has("apiexports") && sets.NewString(rule.Verbs...).Has("bind") {
			return true
		}
		if sets.NewString(rule.Resources...).Has("apiexports/content") {
			return true
		}
	}
	return false
}

func HasMaximalPermissionClaimSubject(crb *rbacv1.ClusterRoleBinding) bool {
	for _, s := range crb.Subjects {
		if strings.HasPrefix(s.Name, apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix) && (s.Kind == rbacv1.UserKind || s.Kind == rbacv1.GroupKind) && s.APIGroup == rbacv1.GroupName {
			return true
		}
	}
	return false
}
