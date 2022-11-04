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
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	byNamespaceLocatorIndexName = "syncer-namespace-ByNamespaceLocator"
	upstreamControllerName      = controllerNameRoot + "-upstream"
)

type UpstreamController struct {
	queue workqueue.RateLimitingInterface

	deleteDownstreamNamespace                  func(ctx context.Context, namespace string) error
	upstreamNamespaceExists                    func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error)
	getDownstreamNamespaceFromNamespaceLocator func(namespaceLocator shared.NamespaceLocator) (runtime.Object, error)
	isDowntreamNamespaceEmpty                  func(ctx context.Context, namespace string) (bool, error)
	namespaceCleaner                           shared.Cleaner

	syncTargetName      string
	syncTargetWorkspace logicalcluster.Name
	syncTargetUID       types.UID
	syncTargetKey       string
}

func NewUpstreamController(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	syncTargetUID types.UID,
	syncerInformers resourcesync.SyncerInformerFactory,
	downstreamClient dynamic.Interface,
	upstreamInformers kcpdynamicinformer.DynamicSharedInformerFactory,
	downstreamInformers dynamicinformer.DynamicSharedInformerFactory,
	namespaceCleaner shared.Cleaner,
) (*UpstreamController, error) {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	logger := logging.WithReconciler(syncerLogger, upstreamControllerName)

	c := UpstreamController{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), upstreamControllerName),

		deleteDownstreamNamespace: func(ctx context.Context, namespace string) error {
			return downstreamClient.Resource(namespaceGVR).Delete(ctx, namespace, metav1.DeleteOptions{})
		},
		upstreamNamespaceExists: func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error) {
			_, err := upstreamInformers.ForResource(namespaceGVR).Lister().ByCluster(clusterName).Get(upstreamNamespaceName)
			if errors.IsNotFound(err) {
				return false, nil
			}
			return !errors.IsNotFound(err), err
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
		isDowntreamNamespaceEmpty: func(ctx context.Context, namespace string) (bool, error) {
			gvrs, err := syncerInformers.SyncableGVRs()
			if err != nil {
				return false, err
			}
			for _, k := range gvrs {
				// Skip namespaces.
				if k.Group == "" && k.Version == "v1" && k.Resource == "namespaces" {
					continue
				}
				gvr := schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
				list, err := downstreamInformers.ForResource(gvr).Lister().ByNamespace(namespace).List(labels.Everything())
				if err != nil {
					return false, err
				}
				if len(list) > 0 {
					return false, nil
				}
			}
			return true, nil
		},
		namespaceCleaner: namespaceCleaner,

		syncTargetName:      syncTargetName,
		syncTargetWorkspace: syncTargetWorkspace,
		syncTargetUID:       syncTargetUID,
		syncTargetKey:       syncTargetKey,
	}

	logger.V(2).Info("Set up upstream namespace informer")

	// React when there's a namespace deletion upstream.
	upstreamInformers.ForResource(namespaceGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	logger.V(2).Info("Set up downstream namespace informer")

	err := downstreamInformers.ForResource(namespaceGVR).Informer().AddIndexers(cache.Indexers{byNamespaceLocatorIndexName: indexByNamespaceLocator})
	if err != nil {
		return nil, err
	}

	return &c, nil
}

func (c *UpstreamController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing namespace")
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *UpstreamController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), upstreamControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *UpstreamController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *UpstreamController) processNextWorkItem(ctx context.Context) bool {
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
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", upstreamControllerName, key, err))
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
