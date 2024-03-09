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

package workspacemounts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	tenancy "github.com/kcp-dev/kcp/sdk/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/tenancy/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/tenancy/v1alpha1"
)

const (
	// ControllerName is the name of this controller.
	ControllerName = "kcp-workspace-mounts"
)

// NewController creates a new controller for generic mounts.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	discoveringDynamicSharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue: queue,

		dynamicClusterClient:                    dynamicClusterClient,
		discoveringDynamicSharedInformerFactory: discoveringDynamicSharedInformerFactory,

		workspaceIndexer: workspaceInformer.Informer().GetIndexer(),
		workspaceLister:  workspaceInformer.Lister(),

		commit: committer.NewCommitter[*tenancyv1alpha1.Workspace, tenancyv1alpha1client.WorkspaceInterface, *tenancyv1alpha1.WorkspaceSpec, *tenancyv1alpha1.WorkspaceStatus](kcpClusterClient.TenancyV1alpha1().Workspaces()),
	}

	_, _ = workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspace(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspace(obj) },
	})

	c.discoveringDynamicSharedInformerFactory.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc:    func(_ schema.GroupVersionResource, obj interface{}) { c.enqueuePotentiallyMountResource(obj) },
		UpdateFunc: func(_ schema.GroupVersionResource, _, obj interface{}) { c.enqueuePotentiallyMountResource(obj) },
		DeleteFunc: nil, // Nothing to do.
	})

	return c, nil
}

type workspaceResource = committer.Resource[*tenancyv1alpha1.WorkspaceSpec, *tenancyv1alpha1.WorkspaceStatus]

// Controller watches Workspaces and dynamically discovered mount resources and reconciles them so
// workspace has right annotations.
type Controller struct {
	// queue is the work-queue used by the controller
	queue workqueue.RateLimitingInterface

	dynamicClusterClient                    kcpdynamic.ClusterInterface
	discoveringDynamicSharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory

	workspaceIndexer cache.Indexer
	workspaceLister  tenancyv1alpha1listers.WorkspaceClusterLister

	// commit creates a patch and submits it, if needed.
	commit func(ctx context.Context, new, old *workspaceResource) error
}

// enqueueWorkspace adds the object to the work queue.
func (c *Controller) enqueueWorkspace(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing Workspace")
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

	workspace, err := c.workspaceLister.Cluster(parent).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
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

// enqueuePotentiallyMountResource looks for workspaces referencing this kind.
func (c *Controller) enqueuePotentiallyMountResource(obj interface{}) {
	u := obj.(*unstructured.Unstructured)

	// We only care about mount objects. All owner objects must have IsMountAnnotationKey set
	// to simplify the logic here. And owner annotation to owner workspaces.
	val, ok := u.GetAnnotations()[tenancyv1alpha1.ExperimentalIsMountAnnotationKey]
	if !ok || val != "true" {
		return
	}

	workspaceOwnerRaw, ok := u.GetAnnotations()[tenancyv1alpha1.ExperimentalMountWorkspaceAnnotationKey]
	if !ok {
		return
	}

	var owner metav1.OwnerReference
	if err := json.Unmarshal([]byte(workspaceOwnerRaw), &owner); err != nil {
		runtime.HandleError(fmt.Errorf("unable to unmarshal owner reference: %w", err))
		return
	}
	if owner.Kind != tenancy.WorkspaceKind {
		runtime.HandleError(fmt.Errorf("owner reference is not a workspace: %s", owner.Kind))
		return
	}

	// queue workspace
	key := kcpcache.ToClusterAwareKey(logicalcluster.From(u).String(), "", owner.Name)
	c.queue.Add(key)
}
