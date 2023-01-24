/*
Copyright 2023 The KCP Authors.

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

package labellogicalcluster

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

type Controller interface {
	EnqueueLogicalCluster(cluster *corev1alpha1.LogicalCluster, values ...interface{})

	Start(ctx context.Context, numThreads int)
}

type LogicalCluster = corev1alpha1.LogicalCluster
type LogicalClusterSpec = corev1alpha1.LogicalClusterSpec
type LogicalClusterStatus = corev1alpha1.LogicalClusterStatus
type Patcher = corev1alpha1client.LogicalClusterInterface
type Resource = committer.Resource[*LogicalClusterSpec, *LogicalClusterStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// NewController returns a new controller for labelling LogicalClusters that should be replicated.
func NewController(
	controllerName string,
	groupName string,
	isRelevantLogicalCluster func(cluster *corev1alpha1.LogicalCluster) bool,
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		controllerName: controllerName,
		groupName:      groupName,

		isRelevantLogicalCluster: isRelevantLogicalCluster,

		queue: queue,

		kcpClusterClient: kcpClusterClient,

		logicalClusterLister:  logicalClusterInformer.Lister(),
		logicalClusterIndexer: logicalClusterInformer.Informer().GetIndexer(),

		commit: committer.NewCommitter[*LogicalCluster, Patcher, *LogicalClusterSpec, *LogicalClusterStatus](kcpClusterClient.CoreV1alpha1().LogicalClusters()),
	}

	logicalClusterInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueLogicalCluster(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueLogicalCluster(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueLogicalCluster(obj)
			},
		},
	})

	return c
}

// controller reconciles ClusterRoles by labelling them to be replicated when pointing to an
// ClusterRole content or verb bind.
type controller struct {
	controllerName string
	groupName      string

	isRelevantLogicalCluster func(cluster *corev1alpha1.LogicalCluster) bool

	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	logicalClusterLister  corev1alpha1listers.LogicalClusterClusterLister
	logicalClusterIndexer cache.Indexer

	// commit creates a patch and submits it, if needed.
	commit CommitFunc
}

func (c *controller) EnqueueLogicalCluster(cluster *corev1alpha1.LogicalCluster, values ...interface{}) {
	c.enqueueLogicalCluster(cluster, values...)
}

// enqueueLogicalCluster enqueues an ClusterRole.
func (c *controller) enqueueLogicalCluster(obj interface{}, values ...interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.controllerName), key)
	logger.V(4).WithValues(values...).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.controllerName)
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

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", c.controllerName, key, err))
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
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false, nil
	}
	obj, err := c.logicalClusterLister.Cluster(parent).Get(name)
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

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
