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

package replication

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-replication-controller"

	// AnnotationKey is the name of the annotation key used to mark an object for replication.
	AnnotationKey = "internal.sharding.kcp.dev/replicate"
)

// NewController returns a new replication controller.
//
// The replication controller copies objects of defined resources that have the "internal.sharding.kcp.dev/replicate" annotation to the cache server.
//
// The replicated object will be placed under the same cluster as the original object.
// In addition to that, all replicated objects will be placed under the shard taken from the shardName argument.
// For example: shards/{shardName}/clusters/{clusterName}/apis/apis.kcp.dev/v1alpha1/apiexports
func NewController(
	shardName string,
	dynamicCacheClient dynamic.ClusterInterface,
	dynamicLocalClient dynamic.ClusterInterface,
	localApiExportInformer apisinformers.APIExportInformer,
	cacheApiExportInformer apisinformers.APIExportInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)
	c := &controller{
		shardName: shardName,
		queue:     queue,

		getCachedAPIExport: func(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
			key := ShardAndLogicalClusterAndNamespaceKey(shardName, cluster.String(), "", name)
			cacheApiExports, err := cacheApiExportInformer.Informer().GetIndexer().ByIndex(ByShardAndLogicalClusterAndNamespaceAndName, key)
			if err != nil {
				return nil, err
			}
			if len(cacheApiExports) == 0 {
				return nil, errors.NewNotFound(apisv1alpha1.Resource("apiexport"), name)
			}
			if len(cacheApiExports) > 1 {
				return nil, fmt.Errorf("expected to find only one instance for the key %s, found %d", key, len(cacheApiExports))
			}
			return cacheApiExports[0].(*apisv1alpha1.APIExport), nil
		},
		getLocalAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
			return localApiExportInformer.Lister().Get(clusters.ToClusterAwareKey(cluster, name))
		},
		getLocalObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			if namespace == "" {
				return dynamicLocalClient.Cluster(cluster).Resource(gvr).Get(ctx, name, metav1.GetOptions{})
			}
			return dynamicLocalClient.Cluster(cluster).Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		},
		createCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if namespace == "" {
				return dynamicCacheClient.Cluster(cluster).Resource(gvr).Create(ctx, object, metav1.CreateOptions{})
			}
			return dynamicCacheClient.Cluster(cluster).Resource(gvr).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
		},
		updateCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if namespace == "" {
				return dynamicCacheClient.Cluster(cluster).Resource(gvr).Update(ctx, object, metav1.UpdateOptions{})
			}
			return dynamicCacheClient.Cluster(cluster).Resource(gvr).Namespace(namespace).Update(ctx, object, metav1.UpdateOptions{})
		},
		deleteCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error {
			if namespace == "" {
				return dynamicCacheClient.Cluster(cluster).Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{})
			}
			return dynamicCacheClient.Cluster(cluster).Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	if err := cacheApiExportInformer.Informer().AddIndexers(cache.Indexers{
		ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
	}); err != nil {
		return nil, err
	}

	localApiExportInformer.Informer().AddEventHandler(c.apiExportInformerEventHandler())
	cacheApiExportInformer.Informer().AddEventHandler(c.apiExportInformerEventHandler())
	return c, nil
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvr := apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")
	gvrKey := fmt.Sprintf("%v::%v", gvr.String(), key)
	c.queue.Add(gvrKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, workers int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	grKey, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(grKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), grKey.(string))
	ctx = klog.NewContext(ctx, logger)
	err := c.reconcile(ctx, grKey.(string))
	if err == nil {
		c.queue.Forget(grKey)
		return true
	}

	runtime.HandleError(fmt.Errorf("%v failed with: %w", grKey, err))
	c.queue.AddRateLimited(grKey)

	return true
}

func (c *controller) apiExportInformerEventHandler() cache.FilteringResourceEventHandler {
	return cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			apiExport, ok := obj.(*apisv1alpha1.APIExport)
			if !ok {
				return false
			}
			_, hasReplicationLabel := apiExport.Annotations[AnnotationKey]
			return hasReplicationLabel
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
			DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
		},
	}
}

type controller struct {
	shardName string
	queue     workqueue.RateLimitingInterface

	getCachedAPIExport func(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	getLocalAPIExport  func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	getLocalObject     func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	createCachedObject func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
	updateCachedObject func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
	deleteCachedObject func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error
}
