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

package heartbeat

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	workloadv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/workload/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/workload/v1alpha1"
)

const ControllerName = "kcp-synctarget-heartbeat"

type Controller struct {
	queue              workqueue.RateLimitingInterface
	kcpClusterClient   kcpclientset.ClusterInterface
	heartbeatThreshold time.Duration
	commit             CommitFunc
	getSyncTarget      func(clusterName logicalcluster.Name, name string) (*workloadv1alpha1.SyncTarget, error)
}

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	heartbeatThreshold time.Duration,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:              queue,
		kcpClusterClient:   kcpClusterClient,
		heartbeatThreshold: heartbeatThreshold,
		commit:             committer.NewCommitter[*SyncTarget, Patcher, *SyncTargetSpec, *SyncTargetStatus](kcpClusterClient.WorkloadV1alpha1().SyncTargets()),
		getSyncTarget: func(clusterName logicalcluster.Name, name string) (*workloadv1alpha1.SyncTarget, error) {
			return syncTargetInformer.Cluster(clusterName).Lister().Get(name)
		},
	}

	_, _ = syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

type SyncTarget = workloadv1alpha1.SyncTarget
type SyncTargetSpec = workloadv1alpha1.SyncTargetSpec
type SyncTargetStatus = workloadv1alpha1.SyncTargetStatus
type Patcher = workloadv1alpha1client.SyncTargetInterface
type Resource = committer.Resource[*SyncTargetSpec, *SyncTargetStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing SyncTarget")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

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

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "error parsing key")
		return nil
	}

	current, err := c.getSyncTarget(clusterName, name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get SyncTarget from lister", "cluster", clusterName, "name", name)
		}

		return nil
	}

	previous := current
	current = current.DeepCopy()

	logger = logging.WithObject(logger, previous)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, key, current); err != nil {
		errs = append(errs, err)
	}

	oldResource := &Resource{ObjectMeta: previous.ObjectMeta, Spec: &previous.Spec, Status: &previous.Status}
	newResource := &Resource{ObjectMeta: current.ObjectMeta, Spec: &current.Spec, Status: &current.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
