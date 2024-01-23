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

package workspacetype

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/tenancy/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/tenancy/v1alpha1"
)

const (
	ControllerName = "kcp-workspacetype"
)

// NewController returns a new controller for WorkspaceTypes.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	workspaceTypeInformer tenancyinformers.WorkspaceTypeClusterInformer,
	shardInformer corev1alpha1informers.ShardClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	shardLister := shardInformer.Lister()
	workspacetypeLister := workspaceTypeInformer.Lister()
	c := &controller{
		queue:               queue,
		kcpClusterClient:    kcpClusterClient,
		workspacetypeLister: workspacetypeLister,
		listShards: func() ([]*corev1alpha1.Shard, error) {
			return shardLister.List(labels.Everything())
		},
		resolveWorkspaceTypes: func(reference tenancyv1alpha1.WorkspaceTypeReference) (*tenancyv1alpha1.WorkspaceType, error) {
			path := logicalcluster.NewPath(reference.Path)
			name := string(reference.Name)
			return indexers.ByPathAndName[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), workspaceTypeInformer.Informer().GetIndexer(), path, name)
		},

		commit: committer.NewCommitter[*WorkspaceType, Patcher, *WorkspaceTypeSpec, *WorkspaceTypeStatus](kcpClusterClient.TenancyV1alpha1().WorkspaceTypes()),
	}

	indexers.AddIfNotPresentOrDie(workspaceTypeInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	_, _ = workspaceTypeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueWorkspaceTypes(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueWorkspaceTypes(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueWorkspaceTypes(obj)
		},
	})

	_, _ = shardInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueAllWorkspaceTypes(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueAllWorkspaceTypes(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueAllWorkspaceTypes(obj)
			},
		},
	)

	return c, nil
}

type WorkspaceType = tenancyv1alpha1.WorkspaceType
type WorkspaceTypeSpec = tenancyv1alpha1.WorkspaceTypeSpec
type WorkspaceTypeStatus = tenancyv1alpha1.WorkspaceTypeStatus
type Patcher = tenancyv1alpha1client.WorkspaceTypeInterface
type Resource = committer.Resource[*WorkspaceTypeSpec, *WorkspaceTypeStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles WorkspaceTypes. It ensures a WorkspaceType has assigned a virtual workspace URL address.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient      kcpclientset.ClusterInterface
	workspacetypeLister   tenancyv1alpha1listers.WorkspaceTypeClusterLister
	listShards            func() ([]*corev1alpha1.Shard, error)
	resolveWorkspaceTypes func(reference tenancyv1alpha1.WorkspaceTypeReference) (*tenancyv1alpha1.WorkspaceType, error)
	commit                CommitFunc
}

// enqueueWorkspaceTypes enqueues a WorkspaceType.
func (c *controller) enqueueWorkspaceTypes(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing WorkspaceType")
	c.queue.Add(key)
}

func (c *controller) enqueueAllWorkspaceTypes(shard interface{}) {
	list, err := c.workspacetypeLister.List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), shard.(*corev1alpha1.Shard))
	for i := range list {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(list[i])
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(4).Info("queuing WorkspaceType because Shard changed")

		c.queue.Add(key)
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

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	obj, err := c.workspacetypeLister.Cluster(clusterName).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	c.reconcile(ctx, obj)

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	return c.commit(ctx, oldResource, newResource)
}
