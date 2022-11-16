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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	tenancyv1beta1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-clusterworkspace"
)

func NewController(
	shardExternalURL func() string,
	kcpClusterClient kcpclient.Interface,
	kubeClusterClient kubernetes.ClusterInterface,
	logicalClusterAdminConfig *rest.Config,
	workspaceInformer tenancyv1beta1informers.WorkspaceInformer,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:                        queue,
		shardExternalURL:             shardExternalURL,
		logicalClusterAdminConfig:    logicalClusterAdminConfig,
		kcpClusterClient:             kcpClusterClient,
		kubeClusterClient:            kubeClusterClient,
		workspaceIndexer:             workspaceInformer.Informer().GetIndexer(),
		workspaceLister:              workspaceInformer.Lister(),
		clusterWorkspaceShardIndexer: clusterWorkspaceShardInformer.Informer().GetIndexer(),
		clusterWorkspaceShardLister:  clusterWorkspaceShardInformer.Lister(),

		commit: committer.NewCommitter[*tenancyv1beta1.Workspace, *tenancyv1beta1.WorkspaceSpec, *tenancyv1beta1.WorkspaceStatus](kcpClusterClient.TenancyV1beta1().Workspaces()),
	}

	indexers.AddIfNotPresentOrDie(workspaceInformer.Informer().GetIndexer(), cache.Indexers{
		unschedulable: indexUnschedulable,
	})
	indexers.AddIfNotPresentOrDie(clusterWorkspaceShardInformer.Informer().GetIndexer(), cache.Indexers{
		byBase36Sha224Name: indexByBase36Sha224Name,
	})

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	clusterWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueShard(obj) },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueShard(obj) },
	})

	return c, nil
}

type workspaceResource = committer.Resource[*tenancyv1beta1.WorkspaceSpec, *tenancyv1beta1.WorkspaceStatus]

// Controller watches Workspaces and WorkspaceShards in order to make sure every ClusterWorkspace
// is scheduled to a valid ClusterWorkspaceShard.
type Controller struct {
	queue workqueue.RateLimitingInterface

	shardExternalURL          func() string
	logicalClusterAdminConfig *rest.Config

	kcpClusterClient  kcpclient.Interface
	kubeClusterClient kubernetes.ClusterInterface
	kcpExternalClient kcpclient.ClusterInterface

	workspaceIndexer cache.Indexer
	workspaceLister  tenancyv1beta1listers.WorkspaceLister

	clusterWorkspaceShardIndexer cache.Indexer
	clusterWorkspaceShardLister  tenancylisters.ClusterWorkspaceShardLister

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *workspaceResource) error
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
			logging.WithQueueKey(logger, key).V(2).Info("queueing unschedulable ClusterWorkspace because of shard update", "clusterWorkspaceShard", shard)
			c.queue.Add(key)
		}
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	// create external client that goes through the front-proxy
	externalConfig := rest.CopyConfig(c.logicalClusterAdminConfig)
	externalConfig.Host = c.shardExternalURL()
	kcpExternalClient, err := kcpclient.NewClusterForConfig(externalConfig)
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

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	workspace, err := c.workspaceLister.Get(key)
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
