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

package namespace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	byNamespaceLocatorIndexName = "syncer-namespace-ByNamespaceLocator"
	controllerName              = "kcp-workload-syncer-namespace"
)

type Controller struct {
	queue workqueue.RateLimitingInterface

	deleteDownstreamNamespace                  func(ctx context.Context, namespace string) error
	upstreamNamespaceExists                    func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error)
	getDownstreamNamespace                     func(name string) (runtime.Object, error)
	getDownstreamNamespaceFromNamespaceLocator func(namespaceLocator shared.NamespaceLocator) (runtime.Object, error)

	syncTargetName      string
	syncTargetWorkspace logicalcluster.Name
	syncTargetUID       types.UID
	syncTargetKey       string
}

func NewNamespaceController(
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	syncTargetUID types.UID,
	upstreamClusterClient dynamic.ClusterInterface,
	downstreamClient dynamic.Interface,
	upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory,
) (*Controller, error) {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	logger := logging.WithReconciler(klog.Background(), controllerName)

	c := Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		deleteDownstreamNamespace: func(ctx context.Context, namespace string) error {
			return downstreamClient.Resource(namespaceGVR).Delete(ctx, namespace, metav1.DeleteOptions{})
		},
		upstreamNamespaceExists: func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error) {
			upstreamNamespaceKey := clusters.ToClusterAwareKey(clusterName, upstreamNamespaceName)
			_, exists, err := upstreamInformers.ForResource(namespaceGVR).Informer().GetIndexer().GetByKey(upstreamNamespaceKey)
			return exists, err
		},
		getDownstreamNamespace: func(downstreamNamespaceName string) (runtime.Object, error) {
			return downstreamInformers.ForResource(namespaceGVR).Lister().Get(downstreamNamespaceName)
		},
		getDownstreamNamespaceFromNamespaceLocator: func(namespaceLocator shared.NamespaceLocator) (runtime.Object, error) {
			namespaceLocatorJSONBytes, err := json.Marshal(namespaceLocator)
			if err != nil {
				return nil, err
			}
			namespaces, err := downstreamInformers.ForResource(namespaceGVR).Informer().GetIndexer().ByIndex(byNamespaceLocatorIndexName, string(namespaceLocatorJSONBytes))
			if err != nil {
				return nil, err
			}
			if len(namespaces) == 0 {
				return nil, nil
			}
			// There should be only one namespace with the same namespace locator, return it.
			return namespaces[0].(*unstructured.Unstructured), nil
		},

		syncTargetName:      syncTargetName,
		syncTargetWorkspace: syncTargetWorkspace,
		syncTargetUID:       syncTargetUID,
		syncTargetKey:       syncTargetKey,
	}

	// React when there's a namespace deletion upstream.
	upstreamInformers.ForResource(namespaceGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	logger.V(2).Info("Set up upstream namespace informer", "syncTargetWorkspace", syncTargetWorkspace, "syncTargetName", syncTargetName, "syncTargetKey", syncTargetKey)

	// Those handlers are for start/resync cases, in case a namespace deletion event is missed, these handlers
	// will make sure that we cleanup the namespace in downstream after restart/resync.
	downstreamInformers.ForResource(namespaceGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.AddToQueue(newObj, logger)
		},
	})

	err := downstreamInformers.ForResource(namespaceGVR).Informer().AddIndexers(cache.Indexers{byNamespaceLocatorIndexName: indexByNamespaceLocator})
	if err != nil {
		return nil, err
	}

	logger.V(2).Info("Set up downstream namespace informer", "syncTargetWorkspace", syncTargetWorkspace, "syncTargetName", syncTargetName, "syncTargetKey", syncTargetKey)

	return &c, nil
}

func (c *Controller) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info("queueing namespace", "key", key)
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	namespaceKey := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), namespaceKey)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, namespaceKey); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// indexByNamespaceLocator is a cache.IndexFunc that indexes namespaces by the namespaceLocator annotation.
func indexByNamespaceLocator(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if loc, found, err := shared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return []string{}, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return []string{}, nil
	} else {
		bs, err := json.Marshal(loc)
		if err != nil {
			return []string{}, fmt.Errorf("failed to marshal locator %#v: %w", loc, err)
		}
		return []string{string(bs)}, nil
	}
}
