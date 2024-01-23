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

package workspace

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/tenancy/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/tenancy/v1alpha1"
)

const (
	ControllerName = "kcp-workspace"
)

func NewController(
	shardName string,
	kcpClusterClient kcpclientset.ClusterInterface,
	kubeClusterClient kubernetes.ClusterInterface,
	logicalClusterAdminConfig *rest.Config,
	externalLogicalClusterAdminConfig *rest.Config,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	globalShardInformer corev1alpha1informers.ShardClusterInformer,
	globalWorkspaceTypeInformer tenancyv1alpha1informers.WorkspaceTypeClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue: queue,

		shardName:                         shardName,
		logicalClusterAdminConfig:         logicalClusterAdminConfig,
		externalLogicalClusterAdminConfig: externalLogicalClusterAdminConfig,

		kcpClusterClient:  kcpClusterClient,
		kubeClusterClient: kubeClusterClient,

		workspaceIndexer: workspaceInformer.Informer().GetIndexer(),
		workspaceLister:  workspaceInformer.Lister(),

		globalShardIndexer: globalShardInformer.Informer().GetIndexer(),
		globalShardLister:  globalShardInformer.Lister(),

		globalWorkspaceTypeIndexer: globalWorkspaceTypeInformer.Informer().GetIndexer(),
		globalWorkspaceTypeLister:  globalWorkspaceTypeInformer.Lister(),

		logicalClusterIndexer: logicalClusterInformer.Informer().GetIndexer(),
		logicalClusterLister:  logicalClusterInformer.Lister(),

		commit: committer.NewCommitter[*tenancyv1alpha1.Workspace, tenancyv1alpha1client.WorkspaceInterface, *tenancyv1alpha1.WorkspaceSpec, *tenancyv1alpha1.WorkspaceStatus](kcpClusterClient.TenancyV1alpha1().Workspaces()),
	}

	indexers.AddIfNotPresentOrDie(workspaceInformer.Informer().GetIndexer(), cache.Indexers{
		unschedulable: indexUnschedulable,
	})
	indexers.AddIfNotPresentOrDie(globalShardInformer.Informer().GetIndexer(), cache.Indexers{
		byBase36Sha224Name: indexByBase36Sha224Name,
	})
	indexers.AddIfNotPresentOrDie(globalWorkspaceTypeInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	_, _ = workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	_, _ = globalShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueShard(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueShard(obj) },
	})

	return c, nil
}

type workspaceResource = committer.Resource[*tenancyv1alpha1.WorkspaceSpec, *tenancyv1alpha1.WorkspaceStatus]

// Controller watches Workspaces and WorkspaceShards in order to make sure every Workspace
// is scheduled to a valid Shard.
type Controller struct {
	queue workqueue.RateLimitingInterface

	shardName                         string
	logicalClusterAdminConfig         *rest.Config // for direct shard connections used during scheduling
	externalLogicalClusterAdminConfig *rest.Config // for front-proxy connections used during initialization

	kcpClusterClient  kcpclientset.ClusterInterface
	kubeClusterClient kubernetes.ClusterInterface
	kcpExternalClient kcpclientset.ClusterInterface

	workspaceIndexer cache.Indexer
	workspaceLister  tenancyv1alpha1listers.WorkspaceClusterLister

	globalShardIndexer cache.Indexer
	globalShardLister  corev1alpha1listers.ShardClusterLister

	globalWorkspaceTypeIndexer cache.Indexer
	globalWorkspaceTypeLister  tenancyv1alpha1listers.WorkspaceTypeClusterLister

	logicalClusterIndexer cache.Indexer
	logicalClusterLister  corev1alpha1listers.LogicalClusterClusterLister

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, old, new *workspaceResource) error
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing Workspace")
	c.queue.Add(key)
}

func (c *Controller) enqueueShard(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	shard, err := c.globalShardLister.Cluster(clusterName).Get(name)
	if err == nil {
		workspaces, err := c.workspaceIndexer.ByIndex(unschedulable, "true")
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
			logging.WithQueueKey(logger, key).V(3).Info("queueing unschedulable Workspace because of shard update", "shard", shard)
			c.queue.Add(key)
		}
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	// create external client that goes through the front-proxy
	externalConfig := rest.CopyConfig(c.externalLogicalClusterAdminConfig)
	kcpExternalClient, err := kcpclientset.NewForConfig(externalConfig)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.kcpExternalClient = kcpExternalClient

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

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
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

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false, nil
	}
	workspace, err := c.workspaceLister.Cluster(parent).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := workspace
	workspace = workspace.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), workspace)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, workspace)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &workspaceResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &workspaceResource{ObjectMeta: workspace.ObjectMeta, Spec: &workspace.Spec, Status: &workspace.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
