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

package clusterworkspacetype

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	controllerName = "kcp-clusterworkspacetype"
)

// NewController returns a new controller for APIExports.
func NewController(
	kcpClusterClient kcpclient.Interface,
	clusterWorkspaceTypeInformer tenancyinformers.ClusterWorkspaceTypeInformer,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	clusterWorkspaceShardLister := clusterWorkspaceShardInformer.Lister()
	clusterWorkspaceTypeLister := clusterWorkspaceTypeInformer.Lister()
	c := &controller{
		queue:                      queue,
		kcpClusterClient:           kcpClusterClient,
		clusterWorkspaceTypeLister: clusterWorkspaceTypeLister,
		listClusterWorkspaceShards: func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
			return clusterWorkspaceShardLister.List(labels.Everything())
		},
		resolveClusterWorkspaceType: func(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
			return clusterWorkspaceTypeLister.Get(keyFor(reference))
		},
	}

	clusterWorkspaceTypeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterWorkspaceType(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueClusterWorkspaceType(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterWorkspaceType(obj)
		},
	})

	clusterWorkspaceShardInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueAllClusterWorkspaceTypes(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueAllClusterWorkspaceTypes(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueAllClusterWorkspaceTypes(obj)
			},
		},
	)

	return c, nil
}

func keyFor(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) string {
	return clusters.ToClusterAwareKey(logicalcluster.New(reference.Path), tenancyv1alpha1.ObjectName(reference.Name))
}

// controller reconciles APIExports. It ensures an export's identity secret exists and is valid.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient            kcpclient.Interface
	clusterWorkspaceTypeLister  tenancylisters.ClusterWorkspaceTypeLister
	listClusterWorkspaceShards  func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error)
	resolveClusterWorkspaceType func(reference tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error)
}

// enqueueClusterWorkspaceType enqueues a ClusterWorkspaceType.
func (c *controller) enqueueClusterWorkspaceType(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logger.V(2).Info("queueing ClusterWorkspaceType")
	c.queue.Add(key)
}

func (c *controller) enqueueAllClusterWorkspaceTypes(clusterWorkspaceShard interface{}) {
	list, err := c.clusterWorkspaceTypeLister.List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), controllerName), clusterWorkspaceShard.(*tenancyv1alpha1.ClusterWorkspaceShard))
	for i := range list {
		key, err := cache.MetaNamespaceKeyFunc(list[i])
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(2).Info("queuing ClusterWorkspaceType because ClusterWorkspaceShard changed")

		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
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
	logger.V(1).Info("processing key")

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	obj, err := c.clusterWorkspaceTypeLister.Get(key)
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
	return c.patchIfNeeded(ctx, old, obj)
}

func (c *controller) patchIfNeeded(ctx context.Context, old, obj *tenancyv1alpha1.ClusterWorkspaceType) error {
	specOrObjectMetaChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec) || !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	if specOrObjectMetaChanged && statusChanged {
		panic("Programmer error: spec and status changed in same reconcile iteration")
	}

	if !specOrObjectMetaChanged && !statusChanged {
		return nil
	}

	clusterWorkspaceTypeForPatch := func(apiExport *tenancyv1alpha1.ClusterWorkspaceType) tenancyv1alpha1.ClusterWorkspaceType {
		var ret tenancyv1alpha1.ClusterWorkspaceType
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

	oldForPatch := clusterWorkspaceTypeForPatch(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.UID = ""
	oldForPatch.ResourceVersion = ""

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for ClusterWorkspaceType %s|%s: %w", clusterName, name, err)
	}

	newForPatch := clusterWorkspaceTypeForPatch(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for ClusterWorkspaceType %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for ClusterWorkspaceType %s|%s: %w", clusterName, name, err)
	}

	var subresources []string
	if statusChanged {
		subresources = []string{"status"}
	}

	_, err = c.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
	return err
}
