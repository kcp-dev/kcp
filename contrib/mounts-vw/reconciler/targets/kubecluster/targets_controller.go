/*
Copyright 2024 The KCP Authors.

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

package kubercluster

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	mountsclientset "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/cluster"
	targetsv1alpha1client "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/typed/targets/v1alpha1"
	targetsv1alpha1informers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions/targets/v1alpha1"
	targetsv1alpha1listers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/listers/targets/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

const (
	// ControllerName is the name of this controller.
	ControllerName = "kcp-targets-kubeclusters"
)

// NewController creates a new controller for targets.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	mountsClusterClient mountsclientset.ClusterInterface,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	targetsInformers targetsv1alpha1informers.TargetKubeClusterClusterInformer,
	store state.ClientSetStoreInterface,
	virtualWorkspaceURL string,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		virtualWorkspaceURL: virtualWorkspaceURL,
		store:               store,

		dynamicClusterClient: dynamicClusterClient,
		kubeClusterClient:    kubeClusterClient,

		targetsIndexer: targetsInformers.Informer().GetIndexer(),
		targetsLister:  targetsInformers.Lister(),

		commit: committer.NewCommitter[*targetsv1alpha1.TargetKubeCluster, targetsv1alpha1client.TargetKubeClusterInterface, *targetsv1alpha1.TargetKubeClusterSpec, *targetsv1alpha1.TargetKubeClusterStatus](mountsClusterClient.TargetsV1alpha1().TargetKubeClusters()),
	}

	_, _ = targetsInformers.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

type targetResource = committer.Resource[*targetsv1alpha1.TargetKubeClusterSpec, *targetsv1alpha1.TargetKubeClusterStatus]

// Controller watches Targets and dynamically discovered mount resources and reconciles them so
// workspace has right annotations.
type Controller struct {
	// queue is the work-queue used by the controller
	queue               workqueue.TypedRateLimitingInterface[string]
	store               state.ClientSetStoreInterface
	virtualWorkspaceURL string

	dynamicClusterClient kcpdynamic.ClusterInterface
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface

	discoveringDynamicSharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory

	targetsIndexer cache.Indexer
	targetsLister  targetsv1alpha1listers.TargetKubeClusterClusterLister

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *targetResource) error
}

// enqueue adds the object to the work queue.
func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing Target")
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
	key := k

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
		return false, err
	}

	target, err := c.targetsLister.Cluster(parent).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := target
	target = target.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), target)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, target)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &targetResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &targetResource{ObjectMeta: target.ObjectMeta, Spec: &target.Spec, Status: &target.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
