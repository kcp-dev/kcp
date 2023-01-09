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

package replicationclusterrolebinding

import (
	"context"
	"fmt"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcprbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	kcpcorehelper "github.com/kcp-dev/kcp/pkg/apis/core/helper"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/replicationclusterrole"
)

const (
	ControllerName = "kcp-apiexport-replication-clusterrolebinding"
)

// NewController returns a new controller for labelling ClusterRoleBinding that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,

		kubeClusterClient: kubeClusterClient,

		clusterRoleBindingLister:  clusterRoleBindingInformer.Lister(),
		clusterRoleBindingIndexer: clusterRoleBindingInformer.Informer().GetIndexer(),

		clusterRoleLister:  clusterRoleInformer.Lister(),
		clusterRoleIndexer: clusterRoleInformer.Informer().GetIndexer(),
	}

	indexers.AddIfNotPresentOrDie(clusterRoleBindingInformer.Informer().GetIndexer(), cache.Indexers{
		replicationclusterrole.ClusterRoleBindingByClusterRoleName: replicationclusterrole.IndexClusterRoleBindingByClusterRoleName,
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

	return c, nil
}

// controller reconciles ClusterRoleBindings by labelling them to be replicated when
// 1. either a maximum-permission-policy subject is bound
// 2. or a ClusterRole.
type controller struct {
	queue workqueue.RateLimitingInterface

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	clusterRoleBindingLister  kcprbaclisters.ClusterRoleBindingClusterLister
	clusterRoleBindingIndexer cache.Indexer

	clusterRoleLister  kcprbaclisters.ClusterRoleClusterLister
	clusterRoleIndexer cache.Indexer
}

// enqueueClusterRoleBinding enqueues an ClusterRoleBinding.
func (c *controller) enqueueClusterRoleBinding(obj interface{}, values ...interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
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

	objs, err := c.clusterRoleBindingIndexer.ByIndex(replicationclusterrole.ClusterRoleBindingByClusterRoleName, key)
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

	crb, err := c.clusterRoleBindingLister.Cluster(cluster).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	logger := logging.WithObject(klog.FromContext(ctx), crb)
	ctx = klog.NewContext(ctx, logger)

	// is a maximum-permission-policy subject?
	replicate := false
	for _, s := range crb.Subjects {
		if strings.HasPrefix(s.Name, apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix) && (s.Kind == rbacv1.UserKind || s.Kind == rbacv1.GroupKind) && s.APIGroup == rbacv1.GroupName {
			replicate = true
			break
		}
	}

	// references relevant ClusterRole?
	if !replicate && crb.RoleRef.Kind == "ClusterRole" && crb.RoleRef.APIGroup == rbacv1.GroupName {
		cr, err := c.clusterRoleLister.Cluster(cluster).Get(crb.RoleRef.Name)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		if cr != nil && replicationclusterrole.HasBindOrContentRule(cr) {
			replicate = true
		}
	}

	// calculate patch
	var value string
	var changed bool
	if replicate {
		if value, changed = kcpcorehelper.ReplicateForValue(crb.Annotations[core.ReplicateAnnotationKey], "apiexport"); !changed {
			return nil
		}
	} else if value, changed = kcpcorehelper.DontReplicateForValue(crb.Annotations[core.ReplicateAnnotationKey], "apiexport"); !changed {
		return nil
	}
	patch := fmt.Sprintf(`{"metadata":{"resourceVersion":%q,"uid":%q,"annotations":{%q:null}}}`, crb.ResourceVersion, crb.UID, core.ReplicateAnnotationKey)
	if value != "" {
		patch = fmt.Sprintf(`{"metadata":{"resourceVersion":%q,"uid":%q,"annotations":{%q:%q}}}`, crb.ResourceVersion, crb.UID, core.ReplicateAnnotationKey, value)
	}

	logger.V(4).WithValues("patch", patch).Info("patching ClusterRoleBinding")
	_, err = c.kubeClusterClient.Cluster(cluster.Path()).RbacV1().ClusterRoleBindings().Patch(ctx, crb.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
