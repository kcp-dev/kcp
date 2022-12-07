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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	configshard "github.com/kcp-dev/kcp/config/shard"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
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
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	remoteShardApiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	configMapInformer kcpcorev1informers.ConfigMapClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		createConfigMap: func(ctx context.Context, cluster logicalcluster.Path, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClusterClient.Cluster(cluster).CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
		},
		getConfigMap: func(clusterName logicalcluster.Name, namespace, name string) (*corev1.ConfigMap, error) {
			return configMapInformer.Lister().Cluster(clusterName).ConfigMaps(namespace).Get(name)
		},
		updateConfigMap: func(ctx context.Context, cluster logicalcluster.Path, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return kubeClusterClient.Cluster(cluster).CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{})
		},
		listAPIExportsFromRemoteShard: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIExport, error) {
			return remoteShardApiExportInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
	}

	remoteShardApiExportInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
			if err != nil {
				runtime.HandleError(err)
				return false
			}
			cluster, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
			if err != nil {
				runtime.HandleError(err)
				return false
			}
			clusterName := logicalcluster.Name(cluster.String()) // TODO: remove when SplitMetaClusterNamespaceKey returns tenancy.Name
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
			key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
			if err != nil {
				runtime.HandleError(err)
				return false
			}
			cluster, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
			if err != nil {
				runtime.HandleError(err)
				return false
			}
			clusterName := logicalcluster.Name(cluster.String()) // TODO: remove when SplitMetaClusterNamespaceKey returns tenancy.Name
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
	queue                         workqueue.RateLimitingInterface
	createConfigMap               func(ctx context.Context, cluster logicalcluster.Path, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)
	getConfigMap                  func(clusterName logicalcluster.Name, namespace, name string) (*corev1.ConfigMap, error)
	updateConfigMap               func(ctx context.Context, cluster logicalcluster.Path, namespace string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)
	listAPIExportsFromRemoteShard func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIExport, error)
}
