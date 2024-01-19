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

package bootstrap

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	ControllerNameBase = "kcp-workspacetypes-bootstrap"
)

func NewController(
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	workspaceType tenancyv1alpha1.WorkspaceTypeReference,
	bootstrap func(context.Context, discovery.DiscoveryInterface, dynamic.Interface, clientset.Interface, sets.Set[string]) error,
	batteriesIncluded sets.Set[string],
) (*controller, error) {
	controllerName := fmt.Sprintf("%s-%s", ControllerNameBase, workspaceType)
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		controllerName:       controllerName,
		queue:                queue,
		dynamicClusterClient: dynamicClusterClient,
		kcpClusterClient:     kcpClusterClient,
		logicalClusterLister: logicalClusterInformer.Lister(),
		workspaceType:        workspaceType,
		bootstrap:            bootstrap,
		batteriesIncluded:    batteriesIncluded,

		commit: committer.NewCommitter[*LogicalCluster, Patcher, *LogicalClusterSpec, *LogicalClusterStatus](kcpClusterClient.CoreV1alpha1().LogicalClusters()),
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

type LogicalCluster = corev1alpha1.LogicalCluster
type LogicalClusterSpec = corev1alpha1.LogicalClusterSpec
type LogicalClusterStatus = corev1alpha1.LogicalClusterStatus
type Patcher = corev1alpha1client.LogicalClusterInterface
type Resource = committer.Resource[*LogicalClusterSpec, *LogicalClusterStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller watches Workspaces of a given type in initializing
// state and bootstrap resources from the configs/<lower-case-type> package.
type controller struct {
	controllerName string
	queue          workqueue.RateLimitingInterface

	dynamicClusterClient kcpdynamic.ClusterInterface
	kcpClusterClient     kcpclientset.ClusterInterface

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	workspaceType     tenancyv1alpha1.WorkspaceTypeReference
	bootstrap         func(context.Context, discovery.DiscoveryInterface, dynamic.Interface, clientset.Interface, sets.Set[string]) error
	batteriesIncluded sets.Set[string]

	commit CommitFunc
}

func (c *controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.controllerName), key)
	logger.V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.controllerName)
	logger = logger.WithValues("workspacetype", c.workspaceType.String())
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", c.controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.logicalClusterLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
