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

package endpoints

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

const (
	ControllerName = "syncer-endpoint-controller"
)

var (
	endpointsGVR  = corev1.SchemeGroupVersion.WithResource("endpoints")
	servicesGVR   = corev1.SchemeGroupVersion.WithResource("services")
	namespacesGVR = corev1.SchemeGroupVersion.WithResource("namespaces")
)

// NewEndpointController returns new controller which would annotate Endpoints related to synced Services, so that those Endpoints
// would be upsynced by the UpSyncer to the upstream KCP workspace.
// This would be useful to enable components such as a KNative controller (running against the KCP workspace) to see the Endpoint,
// and confirm that the related Service is effective.
func NewEndpointController(
	downstreamClient dynamic.Interface,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncTargetClusterName logicalcluster.Name,
	syncTargetName string,
	syncTargetUID types.UID,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),

		syncTargetClusterName: syncTargetClusterName,
		syncTargetName:        syncTargetName,
		syncTargetUID:         syncTargetUID,
		syncTargetKey:         workloadv1alpha1.ToSyncTargetKey(syncTargetClusterName, syncTargetName),

		getDownstreamResource: func(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
			informers, notSynced := ddsifForDownstream.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
			}
			object, err := informer.Lister().ByNamespace(namespace).Get(name)
			if err != nil {
				return nil, err
			}
			unstr, ok := object.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("object type should be *unstructured.Unstructured but was %t", object)
			}
			return unstr, nil
		},
		getDownstreamNamespace: func(name string) (*unstructured.Unstructured, error) {
			informers, notSynced := ddsifForDownstream.Informers()
			informer, ok := informers[namespacesGVR]
			if !ok {
				if shared.ContainsGVR(notSynced, namespacesGVR) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", namespacesGVR)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", namespacesGVR)
			}
			object, err := informer.Lister().Get(name)
			if err != nil {
				return nil, err
			}
			unstr, ok := object.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("object type should be *unstructured.Unstructured but was %t", object)
			}
			return unstr, nil
		},
		patchEndpoint: func(ctx context.Context, namespace, name string, pt types.PatchType, data []byte) error {
			_, err := downstreamClient.Resource(endpointsGVR).Namespace(namespace).Patch(ctx, name, pt, data, metav1.PatchOptions{})
			return err
		},
	}

	informers, _ := ddsifForDownstream.Informers()
	endpointsInformer, ok := informers[endpointsGVR]
	if !ok {
		return nil, errors.New("endpoints informer should be available")
	}

	_, _ = endpointsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueEndpoints(obj)
		},
		UpdateFunc: func(old, new interface{}) {
			c.enqueueEndpoints(new)
		},
	})

	servicesInformer, ok := informers[servicesGVR]
	if !ok {
		return nil, errors.New("endpoints informer should be available")
	}

	_, _ = servicesInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, new interface{}) {
			c.enqueueService(new)
		},
	})

	return c, nil
}

type controller struct {
	queue workqueue.RateLimitingInterface

	syncTargetClusterName logicalcluster.Name
	syncTargetName        string
	syncTargetUID         types.UID
	syncTargetKey         string

	getDownstreamResource  func(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	getDownstreamNamespace func(name string) (*unstructured.Unstructured, error)
	patchEndpoint          func(ctx context.Context, namespace, name string, pt types.PatchType, data []byte) error
}

func (c *controller) enqueueEndpoints(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing")
	c.queue.Add(key)
}

func (c *controller) enqueueService(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "error when queueing from the related service")
	}

	_, err = c.getDownstreamResource(endpointsGVR, namespace, name)
	if kerrors.IsNotFound(err) {
		// no related Endpoints resource => nothing to do
		return
	}
	if err != nil {
		logger.Error(err, "error when queueing from the related service")
	}

	logger.V(2).Info("queueing from service")
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer func() {
		logger.Info("Shutting down controller")
	}()

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	qk := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing", qk)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
