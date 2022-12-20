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

package namespace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	corev1listers "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	schedulingv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
)

const (
	ControllerName      = "kcp-namespace-scheduling-placement"
	byLocationWorkspace = ControllerName + "-byLocationWorkspace"
)

// NewController returns a new controller starting the process of placing namespaces onto locations by creating
// a placement annotation.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	placementInformer schedulingv1alpha1informers.PlacementClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(ns *corev1.Namespace, duration time.Duration) {
			key, err := kcpcache.MetaClusterNamespaceKeyFunc(ns)
			if err != nil {
				runtime.HandleError(err)
				return
			}
			queue.AddAfter(key, duration)
		},

		kubeClusterClient: kubeClusterClient,

		namespaceLister: namespaceInformer.Lister(),

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		byLocationWorkspace: indexByLocationWorkspace,
	}); err != nil {
		return nil, err
	}

	// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
	var namespaceBlocklist = sets.NewString("kube-system", "kube-public", "kube-node-lease", apiexport.DefaultIdentitySecretNamespace)
	namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch ns := obj.(type) {
			case *corev1.Namespace:
				return !namespaceBlocklist.Has(ns.Name)
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    c.enqueueNamespace,
			UpdateFunc: func(_, obj interface{}) { c.enqueueNamespace(obj) },
			DeleteFunc: c.enqueueNamespace,
		},
	})

	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueuePlacement(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueuePlacement(obj) },
	})

	return c, nil
}

// controller
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*corev1.Namespace, time.Duration)

	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	namespaceLister corev1listers.NamespaceClusterLister

	placementLister  schedulingv1alpha1listers.PlacementClusterLister
	placementIndexer cache.Indexer
}

func (c *controller) enqueueNamespace(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing Namespace")
	c.queue.Add(key)
}

func (c *controller) enqueuePlacement(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	nss, err := c.namespaceLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*schedulingv1alpha1.Placement))
	for _, ns := range nss {
		logger = logging.WithObject(logger, ns)

		nsKey, err := kcpcache.MetaClusterNamespaceKeyFunc(ns)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logging.WithQueueKey(logger, nsKey).V(2).Info("queueing Namespace because of Placement")
		c.queue.Add(nsKey)
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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
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

	obj, err := c.namespaceLister.Cluster(clusterName).Get(name)
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

	reconcileErr := c.reconcile(ctx, obj)

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(corev1.Namespace{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for placement %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for LocationDomain %s|%s: %w", clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for LocationDomain %s|%s: %w", clusterName, name, err)
		}
		logger.WithValues("patch", string(patchBytes)).V(2).Info("patching Namespace")
		_, uerr := c.kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Namespaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return reconcileErr
}

func (c *controller) patchNamespace(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	logger := klog.FromContext(ctx)
	logger.WithValues("patch", string(data)).V(2).Info("patching Namespace")
	return c.kubeClusterClient.Cluster(clusterName).CoreV1().Namespaces().Patch(ctx, name, pt, data, opts, subresources...)
}
