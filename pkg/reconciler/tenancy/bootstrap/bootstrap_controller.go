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
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerNameBase = "kcp-clusterworkspacetypes-bootstrap"
)

func NewController(
	dynamicClusterClient kcpdynamic.ClusterInterface,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	thisWorkspaceInformer tenancyv1alpha1informers.ThisWorkspaceClusterInformer,
	workspaceType tenancyv1alpha1.ClusterWorkspaceTypeReference,
	bootstrap func(context.Context, discovery.DiscoveryInterface, dynamic.Interface, clientset.Interface, sets.String) error,
	batteriesIncluded sets.String,
) (*controller, error) {
	controllerName := fmt.Sprintf("%s-%s", ControllerNameBase, workspaceType)
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		controllerName:       controllerName,
		queue:                queue,
		dynamicClusterClient: dynamicClusterClient,
		kcpClusterClient:     kcpClusterClient,
		thisWorkspaceLister:  thisWorkspaceInformer.Lister(),
		workspaceType:        workspaceType,
		bootstrap:            bootstrap,
		batteriesIncluded:    batteriesIncluded,
	}

	thisWorkspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

// controller watches ClusterWorkspaces of a given type in initializing
// state and bootstrap resources from the configs/<lower-case-type> package.
type controller struct {
	controllerName string
	queue          workqueue.RateLimitingInterface

	dynamicClusterClient kcpdynamic.ClusterInterface
	kcpClusterClient     kcpclientset.ClusterInterface

	thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister

	workspaceType     tenancyv1alpha1.ClusterWorkspaceTypeReference
	bootstrap         func(context.Context, discovery.DiscoveryInterface, dynamic.Interface, clientset.Interface, sets.String) error
	batteriesIncluded sets.String
}

func (c *controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.controllerName), key)
	logger.V(2).Info("queueing ThisWorkspace")
	c.queue.Add(key)
}

func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.controllerName)
	logger = logger.WithValues("clusterWorkspaceType", c.workspaceType.String())
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
	logger.V(1).Info("processing key")

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
	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.thisWorkspaceLister.Cluster(clusterName).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(tenancyv1alpha1.ThisWorkspace{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ThisWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.TenancyV1alpha1().ThisWorkspaces().Cluster(clusterName).Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	logger.V(6).Info("processed ThisWorkspace")
	return nil
}
