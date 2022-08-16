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

package identitycache

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	configshard "github.com/kcp-dev/kcp/config/shard"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-api-export-identity-provider"
	workKey        = "key"
	ConfigMapName  = "apiexport-identity-cache"
)

// NewApiExportIdentityProviderController returns a new api export identity provider controller.
//
// The controller reconciles APIExports for the root APIs and
// maintains a config map in the system:shard logical cluster
// with identities per exports specified in the group or the group resources maps.
//
// The config map is meant to be used by clients/informers to inject the identities
// for the given GRs when making requests to the server.
func NewApiExportIdentityProviderController(
	kubeClusterClient kubernetes.ClusterInterface,
	remoteShardApiExportInformer apisinformers.APIExportInformer,
	configMapInformer coreinformers.ConfigMapInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue:                        queue,
		kubeClient:                   kubeClusterClient.Cluster(configshard.SystemShardCluster),
		configMapLister:              configMapInformer.Lister(),
		remoteShardApiExportsIndexer: remoteShardApiExportInformer.Informer().GetIndexer(),
	}

	remoteShardApiExportInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			clusterName, _ := clusters.SplitClusterAwareKey(key)
			return clusterName == tenancyv1alpha1.RootCluster
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.queue.Add(workKey) },
			UpdateFunc: func(old, new interface{}) { c.queue.Add(workKey) },
			DeleteFunc: func(obj interface{}) { c.queue.Add(workKey) },
		},
	})

	configMapInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				runtime.HandleError(err)
				return false
			}
			clusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)
			if clusterName != configshard.SystemShardCluster {
				return false
			}
			switch t := obj.(type) {
			case *corev1.ConfigMap:
				return t.Name == ConfigMapName
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.queue.Add(workKey) },
			UpdateFunc: func(old, new interface{}) { c.queue.Add(workKey) },
			DeleteFunc: func(obj interface{}) { c.queue.Add(workKey) },
		},
	})
	return c, nil
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, _ int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.reconcile(ctx)
	if err == nil {
		c.queue.Forget(key)
		return true
	}

	runtime.HandleError(fmt.Errorf("%v failed with: %w", key, err))
	c.queue.AddRateLimited(key)

	return true
}

type controller struct {
	queue                        workqueue.RateLimitingInterface
	kubeClient                   kubernetes.Interface
	configMapLister              corelisters.ConfigMapLister
	remoteShardApiExportsIndexer cache.Indexer
}
