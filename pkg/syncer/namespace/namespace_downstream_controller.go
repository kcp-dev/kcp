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
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
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
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/third_party/keyfunctions"
)

const (
	controllerNameRoot       = "kcp-workload-syncer-namespace"
	downstreamControllerName = controllerNameRoot + "-downstream"
)

type DownstreamController struct {
	queue workqueue.RateLimitingInterface

	deleteDownstreamNamespace func(ctx context.Context, namespace string) error
	upstreamNamespaceExists   func(clusterName logicalcluster.Name, upstreamNamespaceName string) (bool, error)
	getDownstreamNamespace    func(name string) (runtime.Object, error)
	listDownstreamNamespaces  func() ([]runtime.Object, error)
	createConfigMap           func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)
	updateConfigMap           func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)

	syncTargetName      string
	syncTargetWorkspace logicalcluster.Name
	syncTargetUID       types.UID
	syncTargetKey       string
	dnsNamespace        string
}

func NewDownstreamController(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	syncTargetUID types.UID,
	downstreamConfig *rest.Config,
	downstreamClient dynamic.Interface,
	upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory,
	dnsNamespace string,
) (*DownstreamController, error) {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	logger := logging.WithReconciler(syncerLogger, downstreamControllerName)
	kubeClient := kubernetes.NewForConfigOrDie(downstreamConfig)

	c := DownstreamController{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), downstreamControllerName),

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
		listDownstreamNamespaces: func() ([]runtime.Object, error) {
			return downstreamInformers.ForResource(namespaceGVR).Lister().List(labels.Everything())
		},
		createConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClient.CoreV1().ConfigMaps(configMap.Namespace).Create(ctx, configMap, metav1.CreateOptions{})
		},
		updateConfigMap: func(ctx context.Context, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClient.CoreV1().ConfigMaps(configMap.Namespace).Update(ctx, configMap, metav1.UpdateOptions{})
		},

		syncTargetName:      syncTargetName,
		syncTargetWorkspace: syncTargetWorkspace,
		syncTargetUID:       syncTargetUID,
		syncTargetKey:       syncTargetKey,
		dnsNamespace:        dnsNamespace,
	}

	logger.V(2).Info("Set up downstream namespace informer")

	// Those handlers are for start/resync cases, in case a namespace deletion event is missed, these handlers
	// will make sure that we cleanup the namespace in downstream after restart/resync.
	downstreamInformers.ForResource(namespaceGVR).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.AddToQueue(newObj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	return &c, nil
}

func (c *DownstreamController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := keyfunctions.DeletionHandlingMetaNamespaceKeyFunc(obj) // note: this is *not* a cluster-aware key
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

	logger := logging.WithReconciler(klog.FromContext(ctx), downstreamControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
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
