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

package logicalcluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcprbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	ControllerName = "kcp-tenancy-logicalcluster"

	// WorkspaceAdminClusterRoleBindingName is the name of the cluster role binding created
	// for the owner of the LogicalCluster.
	workspaceAdminClusterRoleBindingName = "workspace-admin"
)

func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:                    queue,
		kubeClusterClient:        kubeClusterClient,
		logicalClusterLister:     logicalClusterInformer.Lister(),
		clusterRoleBindingLister: clusterRoleBindingInformer.Lister(),
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	_, _ = clusterRoleBindingInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crb, ok := obj.(*rbacv1.ClusterRoleBinding)
			if !ok {
				return false
			}
			return crb.Name == workspaceAdminClusterRoleBindingName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueCRB(obj) },
			UpdateFunc: func(obj, _ interface{}) { c.enqueueCRB(obj) },
			DeleteFunc: func(obj interface{}) { c.enqueueCRB(obj) },
		},
	})

	return c
}

// Controller creates a ClusterRoleBinding workspace-admin for the owner of the LogicalCluster.
type Controller struct {
	queue workqueue.RateLimitingInterface

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	clusterRoleBindingLister kcprbaclisters.ClusterRoleBindingClusterLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

func (c *Controller) enqueueCRB(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
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

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "unable to decode key")
		return nil
	}

	logicalCluster, err := c.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get LogicalCluster from lister", "cluster", clusterName)
		}

		return nil // nothing we can do here
	}

	logger = logging.WithObject(logger, logicalCluster)
	ctx = klog.NewContext(ctx, logger)

	if !logicalCluster.DeletionTimestamp.IsZero() {
		logger.V(4).Info("LogicalCluster is being deleted, skipping")
		return nil
	}

	// need to create
	ownerAnnotation, found := logicalCluster.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]
	if !found {
		// no owner - can't create
		return nil
	}

	var userInfo authenticationv1.UserInfo
	if err = json.Unmarshal([]byte(ownerAnnotation), &userInfo); err != nil {
		logger.Error(err, "failed to unmarshal owner annotation on LogicalCluster", "key", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, "value", ownerAnnotation, "clusterName", clusterName)
		// can't do anything further and requeuing won't help
		return nil
	}

	newBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: workspaceAdminClusterRoleBindingName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     userInfo.Username,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}

	old, err := c.clusterRoleBindingLister.Cluster(clusterName).Get(workspaceAdminClusterRoleBindingName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		_, err = c.kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, newBinding, metav1.CreateOptions{})
		return err
	}

	if equality.Semantic.DeepEqual(old.Subjects, newBinding.Subjects) == equality.Semantic.DeepEqual(old.RoleRef, newBinding.RoleRef) {
		return nil
	}

	old.Subjects = newBinding.Subjects
	old.RoleRef = newBinding.RoleRef
	_, err = c.kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoleBindings().Update(ctx, newBinding, metav1.UpdateOptions{})
	return err
}
