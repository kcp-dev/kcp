/*
Copyright 2026 The kcp Authors.

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

package apiexporthistory

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	sdkclient "github.com/kcp-dev/sdk/client"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
)

const ControllerName = "kcp-apiexport-history"

// NewController returns a controller recording the resource scopes of APIExports.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	historyInformer apisv1alpha2informers.APIExportHistoryClusterInformer,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			return apiExportInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportsByAPIResourceSchema: func(key string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), indexers.APIExportByAPIResourceSchema, key)
		},
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemaInformer.Lister().Cluster(clusterName).Get(name)
		},
		getHistory: func(name string) (*apisv1alpha2.APIExportHistory, error) {
			return historyInformer.Lister().Cluster(apibinding.SystemBoundCRDsClusterName).Get(name)
		},
		getHistoriesByAPIExport: func(key string) ([]*apisv1alpha2.APIExportHistory, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExportHistory](historyInformer.Informer().GetIndexer(), indexers.APIExportHistoryByAPIExport, key)
		},
		createHistory: func(ctx context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error) {
			return kcpClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApisV1alpha2().APIExportHistories().Create(ctx, history, metav1.CreateOptions{})
		},
		updateHistoryStatus: func(ctx context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error) {
			return kcpClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApisV1alpha2().APIExportHistories().UpdateStatus(ctx, history, metav1.UpdateOptions{})
		},
		deleteHistory: func(ctx context.Context, name string) error {
			return kcpClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApisV1alpha2().APIExportHistories().Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	_, _ = apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
	})

	_, _ = apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj) },
	})

	_, _ = historyInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueHistory(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueHistory(obj) },
	}))

	return c, nil
}

type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getAPIExport                     func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsByAPIResourceSchema func(key string) ([]*apisv1alpha2.APIExport, error)
	getAPIResourceSchema             func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getHistory                       func(name string) (*apisv1alpha2.APIExportHistory, error)
	getHistoriesByAPIExport          func(key string) ([]*apisv1alpha2.APIExportHistory, error)
	createHistory                    func(ctx context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error)
	updateHistoryStatus              func(ctx context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error)
	deleteHistory                    func(ctx context.Context, name string) error
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing APIExport")
	c.queue.Add(key)
}

// enqueueAPIResourceSchema queues the APIExports referencing a newly created schema.
func (c *controller) enqueueAPIResourceSchema(obj interface{}) {
	schema, ok := obj.(*apisv1alpha1.APIResourceSchema)
	if !ok {
		return
	}

	key := sdkclient.ToClusterAwareKey(logicalcluster.From(schema).Path(), schema.Name)
	apiExports, err := c.getAPIExportsByAPIResourceSchema(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, apiExport := range apiExports {
		c.enqueueAPIExport(apiExport)
	}
}

// enqueueHistory requeues the owning APIExport when its history appears or is removed.
func (c *controller) enqueueHistory(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	history, ok := obj.(*apisv1alpha2.APIExportHistory)
	if !ok {
		return
	}

	key := kcpcache.ToClusterAwareKey(history.Spec.APIExport.Cluster, "", history.Spec.APIExport.Name)
	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing APIExport via scope history")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}
	clusterName := logicalcluster.Name(cluster.String())

	apiExport, err := c.getAPIExport(clusterName, name)
	if apierrors.IsNotFound(err) {
		return c.deleteHistories(ctx, clusterName, name)
	} else if err != nil {
		return err
	}

	return c.reconcile(ctx, apiExport)
}

// InstallIndexers adds the indexers this controller requires to the informers.
func InstallIndexers(
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	historyInformer apisv1alpha2informers.APIExportHistoryClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportByAPIResourceSchema: indexers.IndexAPIExportByAPIResourceSchema,
	})
	indexers.AddIfNotPresentOrDie(historyInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportHistoryByAPIExport: indexers.IndexAPIExportHistoryByAPIExport,
	})
}
