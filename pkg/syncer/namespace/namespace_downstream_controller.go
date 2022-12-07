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
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
)

const (
	controllerNameRoot       = "kcp-workload-syncer-namespace"
	downstreamControllerName = controllerNameRoot + "-downstream"
)

type DownstreamController struct {
	queue        workqueue.RateLimitingInterface
	delayedQueue workqueue.RateLimitingInterface

	lock                sync.Mutex
	toDeleteMap         map[string]time.Time
	namespaceCleanDelay time.Duration

	deleteDownstreamNamespace func(ctx context.Context, namespace string) error
	upstreamNamespaceExists   func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error)
	getDownstreamNamespace    func(name string) (runtime.Object, error)
	listDownstreamNamespaces  func() ([]runtime.Object, error)
	isDowntreamNamespaceEmpty func(ctx context.Context, namespace string) (bool, error)
	createConfigMap           func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)
	updateConfigMap           func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)

	syncTargetName        string
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
	syncTargetKey         string
	dnsNamespace          string
}

func NewDownstreamController(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	syncTargetUID types.UID,
	syncerInformers resourcesync.SyncerInformerFactory,
	downstreamConfig *rest.Config,
	downstreamClient dynamic.Interface,
	upstreamInformers kcpdynamicinformer.DynamicSharedInformerFactory,
	downstreamInformers dynamicinformer.DynamicSharedInformerFactory,
	dnsNamespace string,
	namespaceCleanDelay time.Duration,
) (*DownstreamController, error) {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	logger := logging.WithReconciler(syncerLogger, downstreamControllerName)
	kubeClient := kubernetes.NewForConfigOrDie(downstreamConfig)

	c := DownstreamController{
		queue:        workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), downstreamControllerName),
		delayedQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), downstreamControllerName),
		toDeleteMap:  make(map[string]time.Time),
		deleteDownstreamNamespace: func(ctx context.Context, namespace string) error {
			return downstreamClient.Resource(namespaceGVR).Delete(ctx, namespace, metav1.DeleteOptions{})
		},
		upstreamNamespaceExists: func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error) {
			_, err := upstreamInformers.ForResource(namespaceGVR).Lister().ByCluster(clusterName).Get(upstreamNamespaceName)
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return !apierrors.IsNotFound(err), err
		},
		getDownstreamNamespace: func(downstreamNamespaceName string) (runtime.Object, error) {
			return downstreamInformers.ForResource(namespaceGVR).Lister().Get(downstreamNamespaceName)
		},
		listDownstreamNamespaces: func() ([]runtime.Object, error) {
			return downstreamInformers.ForResource(namespaceGVR).Lister().List(labels.Everything())
		},
		isDowntreamNamespaceEmpty: func(ctx context.Context, namespace string) (bool, error) {
			gvrs, err := syncerInformers.SyncableGVRs()
			if err != nil {
				return false, err
			}
			for k, v := range gvrs {
				// Skip namespaces.
				if k.Group == "" && k.Version == "v1" && k.Resource == "namespaces" {
					continue
				}
				list, err := v.DownstreamInformer.Lister().ByNamespace(namespace).List(labels.Everything())
				if err != nil {
					return false, err
				}
				if len(list) > 0 {
					return false, nil
				}
			}
			return true, nil
		},
		createConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClient.CoreV1().ConfigMaps(configMap.Namespace).Create(ctx, configMap, metav1.CreateOptions{})
		},
		updateConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClient.CoreV1().ConfigMaps(configMap.Namespace).Update(ctx, configMap, metav1.UpdateOptions{})
		},

		syncTargetName:        syncTargetName,
		syncTargetClusterName: syncTargetWorkspace,
		syncTargetUID:         syncTargetUID,
		syncTargetKey:         syncTargetKey,
		dnsNamespace:          dnsNamespace,

		namespaceCleanDelay: namespaceCleanDelay,
	}

	logger.V(2).Info("Set up downstream namespace informer")

	// Those handlers are for start/resync cases, in case a namespace deletion event is missed, these handlers
	// will make sure that we cleanup the namespace in downstream after restart/resync.
	downstreamInformers.ForResource(namespaceGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	return &c, nil
}

func (c *DownstreamController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj) // note: this is *not* a cluster-aware key
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing namespace")
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *DownstreamController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer c.delayedQueue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), downstreamControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
		go wait.UntilWithContext(ctx, c.startDelayedWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *DownstreamController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *DownstreamController) processNextWorkItem(ctx context.Context) bool {
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
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", downstreamControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

func (c *DownstreamController) isPlannedForCleaning(key string) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	_, ok := c.toDeleteMap[key]
	return ok
}

func (c *DownstreamController) CancelCleaning(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.toDeleteMap, key)
}

func (c *DownstreamController) PlanCleaning(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	now := time.Now()
	if plannedFor, planned := c.toDeleteMap[key]; !planned || now.After(plannedFor) {
		c.toDeleteMap[key] = now.Add(c.namespaceCleanDelay)
		c.delayedQueue.AddAfter(key, c.namespaceCleanDelay)
	}
}

func (c *DownstreamController) startDelayedWorker(ctx context.Context) {
	logger := klog.FromContext(ctx).WithValues("queue", "delayed")
	ctx = klog.NewContext(ctx, logger)
	for c.processNextDelayedWorkItem(ctx) {
	}
}

func (c *DownstreamController) processNextDelayedWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.delayedQueue.Get()
	if quit {
		return false
	}
	namespaceKey := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), namespaceKey)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.delayedQueue.Done(key)

	if err := c.processDelayed(ctx, namespaceKey); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", downstreamControllerName, key, err))
		c.delayedQueue.AddRateLimited(key)
		return true
	}
	c.delayedQueue.Forget(key)
	return true
}

func (c *DownstreamController) processDelayed(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	if !c.isPlannedForCleaning(key) {
		logger.V(2).Info("Namespace is not marked for deletion check anymore, skipping")
		return nil
	}

	empty, err := c.isDowntreamNamespaceEmpty(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to check if downstream namespace is empty: %w", err)
	}
	if !empty {
		logger.V(2).Info("Namespace is not empty, skip cleaning now but keep it as a candidate for future cleaning")
		return nil
	}

	err = c.deleteDownstreamNamespace(ctx, key)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		logger.V(2).Info("Namespace is not found, perhaps it was already deleted")
	}
	c.CancelCleaning(key)
	return nil
}
