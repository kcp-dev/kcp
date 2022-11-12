/*
Copyright 2021 The KCP Authors.

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

package thisworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	rbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	"github.com/kcp-dev/client-go/kubernetes"
	rbaclisters "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	tenancyv1beta1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-thisworkspace"
)

func NewController(
	kcpClusterClient kcpclient.Interface,
	kubeClusterClient kubernetes.ClusterInterface,
	logicalClusterAdminConfig *rest.Config,
	clusterWorkspaceInformer tenancyinformers.ClusterWorkspaceInformer,
	workspaceInformer tenancyv1beta1informers.WorkspaceInformer,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardInformer,
	apiBindingsInformer apisinformers.APIBindingInformer,
	thisWorkspaceInformer tenancyinformers.ThisWorkspaceInformer,
	clusterRoleBindingInformer rbacinformers.ClusterRoleBindingClusterInformer,
	clusterRoleInformer rbacinformers.ClusterRoleClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:                        queue,
		logicalClusterAdminConfig:    logicalClusterAdminConfig,
		kcpClusterClient:             kcpClusterClient,
		kubeClusterClient:            kubeClusterClient,
		clusterWorkspaceIndexer:      clusterWorkspaceInformer.Informer().GetIndexer(),
		clusterWorkspaceLister:       clusterWorkspaceInformer.Lister(),
		workspaceIndexer:             workspaceInformer.Informer().GetIndexer(),
		workspaceLister:              workspaceInformer.Lister(),
		clusterWorkspaceShardIndexer: clusterWorkspaceShardInformer.Informer().GetIndexer(),
		clusterWorkspaceShardLister:  clusterWorkspaceShardInformer.Lister(),
		apiBindingIndexer:            apiBindingsInformer.Informer().GetIndexer(),
		apiBindingLister:             apiBindingsInformer.Lister(),
		thisWorkspaceIndexer:         thisWorkspaceInformer.Informer().GetIndexer(),
		thisWorkspaceLister:          thisWorkspaceInformer.Lister(),
		clusterRoleBindingIndexer:    clusterRoleBindingInformer.Informer().GetIndexer(),
		clusterRoleBindingLister:     clusterRoleBindingInformer.Lister(),
		clusterRoleIndexer:           clusterRoleInformer.Informer().GetIndexer(),
		clusterRoleLister:            clusterRoleInformer.Lister(),
	}

	indexers.AddIfNotPresentOrDie(apiBindingsInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalCluster: indexers.IndexByLogicalCluster,
	})
	indexers.AddIfNotPresentOrDie(clusterRoleBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalCluster: indexers.IndexByLogicalCluster,
	})
	indexers.AddIfNotPresentOrDie(clusterWorkspaceInformer.Informer().GetIndexer(), cache.Indexers{
		byCurrentShard: indexByCurrentShard,
	})
	indexers.AddIfNotPresentOrDie(clusterWorkspaceInformer.Informer().GetIndexer(), cache.Indexers{
		unschedulable: indexUnschedulable,
	})
	indexers.AddIfNotPresentOrDie(clusterWorkspaceShardInformer.Informer().GetIndexer(), cache.Indexers{
		byBase36Sha224Name: indexByBase36Sha224Name,
	})

	clusterWorkspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	clusterWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueShard(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueShard(obj) },
	})

	apiBindingsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueBinding(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueBinding(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueBinding(obj) },
	})

	thisWorkspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueThisWorkspace(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueThisWorkspace(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueThisWorkspace(obj) },
	})

	clusterRoleBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueClusterRoleBinding(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueClusterRoleBinding(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueClusterRoleBinding(obj) },
	})

	clusterRoleInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueClusterRole(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueClusterRole(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueClusterRole(obj) },
	})

	return c, nil
}

// Controller watches Workspaces and WorkspaceShards in order to make sure every ClusterWorkspace
// is scheduled to a valid ClusterWorkspaceShard.
type Controller struct {
	queue workqueue.RateLimitingInterface

	logicalClusterAdminConfig *rest.Config

	kcpClusterClient  kcpclient.Interface
	kubeClusterClient kubernetes.ClusterInterface

	clusterWorkspaceIndexer cache.Indexer
	clusterWorkspaceLister  tenancylisters.ClusterWorkspaceLister

	workspaceIndexer cache.Indexer
	workspaceLister  tenancyv1beta1listers.WorkspaceLister

	clusterWorkspaceShardIndexer cache.Indexer
	clusterWorkspaceShardLister  tenancylisters.ClusterWorkspaceShardLister

	apiBindingIndexer cache.Indexer
	apiBindingLister  apislisters.APIBindingLister

	thisWorkspaceIndexer cache.Indexer
	thisWorkspaceLister  tenancylisters.ThisWorkspaceLister

	clusterRoleBindingIndexer cache.Indexer
	clusterRoleBindingLister  rbaclisters.ClusterRoleBindingClusterLister

	clusterRoleIndexer cache.Indexer
	clusterRoleLister  rbaclisters.ClusterRoleClusterLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing ClusterWorkspace")
	c.queue.Add(key)
}

func (c *Controller) enqueueShard(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	shard, err := c.clusterWorkspaceShardLister.Get(key)
	if err == nil {
		workspaces, err := c.clusterWorkspaceIndexer.ByIndex(unschedulable, "true")
		if err != nil {
			runtime.HandleError(err)
			return
		}
		for _, workspace := range workspaces {
			key, err := kcpcache.MetaClusterNamespaceKeyFunc(workspace)
			if err != nil {
				runtime.HandleError(err)
				return
			}
			logging.WithQueueKey(logger, key).V(2).Info("queueing unschedulable ClusterWorkspace because of shard update", "clusterWorkspaceShard", shard)
			c.queue.Add(key)
		}
	}

	_, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	workspaces, err := c.clusterWorkspaceIndexer.ByIndex(byCurrentShard, name)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, workspace := range workspaces {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(workspace)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing ClusterWorkspace on shard", "clusterWorkspaceShard", name)
		c.queue.Add(key)
	}
}

func (c *Controller) enqueueBinding(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if clusterName == tenancyv1alpha1.RootCluster {
		return
	}
	parent, ws := clusterName.Split()

	queueKey := client.ToClusterAwareKey(parent, ws)
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), queueKey)
	logger.V(2).Info("queueing initializing ClusterWorkspace because APIBinding changed", "APIBinding", key)
	c.queue.Add(queueKey)
}

func (c *Controller) enqueueThisWorkspace(obj interface{}) {
	twKey, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(twKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	key := kcpcache.ToClusterAwareKey(clusterName.String(), "", name)
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing ClusterWorkspace", "reason", "ThisWorkspace changed", "reasonKey", twKey)
	c.queue.Add(key)
}

func (c *Controller) enqueueClusterRoleBinding(obj interface{}) {
	crbKey, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(crbKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	cws, err := c.clusterWorkspaceIndexer.ByIndex(indexers.ByLogicalCluster, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, cw := range cws {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(cw)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
		logging.WithQueueKey(logger, key).V(2).Info("queueing ClusterWorkspace", "reason", "ClusterRoleBinding changed", "reasonKey", crbKey)
		c.queue.Add(key)
	}
}

func (c *Controller) enqueueClusterRole(obj interface{}) {
	crKey, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(crKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	cws, err := c.clusterWorkspaceIndexer.ByIndex(indexers.ByLogicalCluster, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, cw := range cws {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(cw)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
		logging.WithQueueKey(logger, key).V(2).Info("queueing ClusterWorkspace", "reason", "ClusterRole changed", "reasonKey", crKey)
		c.queue.Add(key)
	}
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
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) patchIfNeeded(ctx context.Context, old, obj *tenancyv1alpha1.ClusterWorkspace) error {
	logger := klog.FromContext(ctx)
	specOrObjectMetaChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec) || !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	if !specOrObjectMetaChanged && !statusChanged {
		return nil
	}

	forPatch := func(apiExport *tenancyv1alpha1.ClusterWorkspace) tenancyv1alpha1.ClusterWorkspace {
		var ret tenancyv1alpha1.ClusterWorkspace
		if specOrObjectMetaChanged {
			ret.ObjectMeta = apiExport.ObjectMeta
			ret.Spec = apiExport.Spec
		} else {
			ret.Status = apiExport.Status
		}
		return ret
	}

	clusterName := logicalcluster.From(old)
	name := old.Name

	oldForPatch := forPatch(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.UID = ""
	oldForPatch.ResourceVersion = ""

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	newForPatch := forPatch(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	var subresources []string
	if statusChanged {
		subresources = []string{"status"}
	}

	logger.WithValues("patch", string(patchBytes)).V(2).Info("patching ClusterWorkspace")
	_, err = c.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
	if err != nil {
		return fmt.Errorf("failed to patch ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	if specOrObjectMetaChanged && statusChanged {
		// enqueue again to take care of the spec change, assuming the patch did nothing
		return fmt.Errorf("programmer error: spec and status changed in same reconcile iteration:\n%s", cmp.Diff(old, obj))
	}

	return nil
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	obj, err := c.clusterWorkspaceLister.Get(key)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, obj)
	if err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	if err := c.patchIfNeeded(ctx, old, obj); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
