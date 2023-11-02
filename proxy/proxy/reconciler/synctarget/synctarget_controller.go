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

package synctarget

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxyv1alpha1client "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/typed/proxy/v1alpha1"
	proxyv1alpha1informers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions/proxy/v1alpha1"
	proxyv1alpha1listers "github.com/kcp-dev/kcp/proxy/client/listers/proxy/v1alpha1"
	"github.com/kcp-dev/kcp/proxy/reconciler/committer"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "workspace-proxy-controller"
)

type controller struct {
	queue workqueue.RateLimitingInterface

	workspaceProxyUID    types.UID
	workspaceProxyLister proxyv1alpha1listers.WorkspaceProxyLister
	commit               CommitFunc

	reconcilers []reconciler
}

// NewWorkspaceProxyController returns a controller that watches the [proxyv1alpha1.WorkspaceProxy]
// associated to this proxy.
// It then calls the update methods on the shardManager.
func NewWorkspaceProxyController(
	workspaceProxyLogger logr.Logger,
	workspaceProxyClient proxyv1alpha1client.WorkspaceProxyInterface,
	workspaceProxyInformer proxyv1alpha1informers.WorkspaceProxyInformer,
	workspaceProxyName string,
	workspaceProxyClusterName logicalcluster.Name,
	workspaceProxyUID types.UID,
	shardManager *shardManager,
	startShardTunneler func(ctx context.Context, shardURL proxyv1alpha1.TunnelWorkspace),
) (*controller, error) {
	c := &controller{
		queue:                workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		workspaceProxyUID:    workspaceProxyUID,
		workspaceProxyLister: workspaceProxyInformer.Lister(),
		commit:               committer.NewCommitterScoped[*WorkspaceProxy, Patcher, *WorkspaceProxySpec, *WorkspaceProxyStatus](workspaceProxyClient),

		reconcilers: []reconciler{
			shardManager,
			&tunnelerReconciler{
				startedTunnelers:   make(map[proxyv1alpha1.TunnelWorkspace]tunnelerStopper),
				startShardTunneler: startShardTunneler,
			},
		},
	}

	logger := logging.WithReconciler(workspaceProxyLogger, controllerName)

	_, _ = workspaceProxyInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, name, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				return false
			}
			return name == workspaceProxyName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(obj, logger) },
			UpdateFunc: func(old, obj interface{}) { c.enqueue(obj, logger) },
			DeleteFunc: func(obj interface{}) { c.enqueue(obj, logger) },
		},
	})

	return c, nil
}

type WorkspaceProxy = proxyv1alpha1.WorkspaceProxy
type WorkspaceProxySpec = proxyv1alpha1.WorkspaceProxySpec
type WorkspaceProxyStatus = proxyv1alpha1.WorkspaceProxyStatus
type Patcher = proxyv1alpha1client.WorkspaceProxyInterface
type Resource = committer.Resource[*WorkspaceProxySpec, *WorkspaceProxyStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

func (c *controller) enqueue(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing WorkspaceProxy")

	c.queue.Add(key)
}

// Start starts the controller worker.
func (c *controller) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

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
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
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
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return false, nil
	}

	workspaceProxy, err := c.workspaceProxyLister.Get(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if apierrors.IsNotFound(err) || workspaceProxy.GetUID() != c.workspaceProxyUID {
		return c.reconcile(ctx, nil)
	}

	previous := workspaceProxy
	workspaceProxy = workspaceProxy.DeepCopy()

	var errs []error
	requeue, err := c.reconcile(ctx, workspaceProxy)
	if err != nil {
		errs = append(errs, err)
	}

	oldResource := &Resource{ObjectMeta: previous.ObjectMeta, Spec: &previous.Spec, Status: &previous.Status}
	newResource := &Resource{ObjectMeta: workspaceProxy.ObjectMeta, Spec: &workspaceProxy.Spec, Status: &workspaceProxy.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, errors.NewAggregate(errs)
}
