/*
Copyright 2025 The KCP Authors.

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

package apiexportendpointsliceurls

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	apisv1alpha1apply "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-apiexportendpointslice-urls"
)

// NewController returns a new controller for APIExportEndpointSlices.
// Shards and APIExports are read from the cache server.
func NewController(
	shardName string,
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	globalAPIExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
	globalAPIExportClusterInformer apisv1alpha1informers.APIExportClusterInformer,
	clusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	c := &controller{
		shardName:     shardName,
		clusterClient: clusterClient,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getMyShard: func() (*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Cluster(core.RootCluster).Lister().Get(shardName)
		},
		getAPIExportEndpointSlice: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error) {
			obj, err := indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExportEndpointSlice](apisv1alpha1.Resource("apiexportendpointslices"), apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), path, name)
			if err != nil {
				return nil, err
			}
			return obj, err
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportClusterInformer.Informer().GetIndexer(), path, name)
		},
		listAPIBindingsByAPIExport: func(export *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
			// binding keys by full path
			keys := sets.New[string]()
			if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
				pathKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
				if err != nil {
					return nil, err
				}
				keys.Insert(pathKeys...)
			}

			clusterKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
			if err != nil {
				return nil, err
			}
			keys.Insert(clusterKeys...)

			bindings := make([]*apisv1alpha1.APIBinding, 0, keys.Len())
			for _, key := range sets.List[string](keys) {
				binding, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil {
					utilruntime.HandleError(err)
					continue
				} else if !exists {
					utilruntime.HandleError(fmt.Errorf("APIBinding %q does not exist", key))
					continue
				}
				bindings = append(bindings, binding.(*apisv1alpha1.APIBinding))
			}
			return bindings, nil
		},
		patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
			_, err := clusterClient.ApisV1alpha1().APIExportEndpointSlices().Cluster(cluster).ApplyStatus(ctx, patch, metav1.ApplyOptions{
				FieldManager: shardName,
			})
			return err
		},
		apiExportEndpointSliceClusterInformer:       apiExportEndpointSliceClusterInformer,
		globalApiExportEndpointSliceClusterInformer: globalAPIExportEndpointSliceClusterInformer,
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = apiExportEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](obj), logger, "")
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](newObj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](obj), logger, "")
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](obj), logger)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSliceByAPIBinding(objOrTombstone[*apisv1alpha1.APIBinding](obj), logger)
		},
	})

	_, _ = globalAPIExportEndpointSliceClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](obj), logger, " from cache")
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](newObj), logger, " from cache")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](obj), logger, " from cache")
		},
	}))

	return c, nil
}

// controller reconciles APIExportEndpointSlices. It ensures that the shard endpoints are populated
// in the status of every APIExportEndpointSlices.
type controller struct {
	queue         workqueue.TypedRateLimitingInterface[string]
	shardName     string
	clusterClient kcpclientset.ClusterInterface

	getMyShard                  func() (*corev1alpha1.Shard, error)
	getAPIExportEndpointSlice   func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExport                func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	listAPIBindingsByAPIExport  func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error)
	patchAPIExportEndpointSlice func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error

	apiExportEndpointSliceClusterInformer       apisv1alpha1informers.APIExportEndpointSliceClusterInformer
	globalApiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer
}

func (c *controller) enqueueAPIExportEndpointSliceByAPIBinding(binding *apisv1alpha1.APIBinding, logger logr.Logger) {
	{ // local to shard
		keys := sets.New[string]()
		if path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path); !path.Empty() { // This is remote apibinding.
			pathKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, path.Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(pathKeys...)
		} else {
			// This is local apibinding to the export. Meaning it has path set to empty string, so apiexport is in the same cluster as the binding.
			// While our CLI does not allow this, it is possible to create such a binding via the API.
			clusterKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, logicalcluster.From(binding).Path().Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(clusterKeys...)
		}

		for _, key := range sets.List[string](keys) {
			slice, exists, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
			if err != nil {
				utilruntime.HandleError(err)
				continue
			} else if !exists {
				continue
			}
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](slice), logger, " because of APIBinding")
		}
	}
	{
		keys := sets.New[string]()
		if path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path); !path.Empty() {
			pathKeys, err := c.globalApiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, path.Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(pathKeys...)
		} else {
			clusterKeys, err := c.globalApiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, logicalcluster.From(binding).Path().Join(binding.Spec.Reference.Export.Name).String())
			if err != nil {
				utilruntime.HandleError(err)
				return
			}
			keys.Insert(clusterKeys...)
		}

		for _, key := range sets.List[string](keys) {
			slice, exists, err := c.globalApiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
			if err != nil {
				utilruntime.HandleError(err)
				continue
			} else if !exists {
				continue
			}
			c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](slice), logger, "because of APIBinding from cache")
		}
	}
}

// enqueueAPIExportEndpointSlice enqueues an APIExportEndpointSlice.
func (c *controller) enqueueAPIExportEndpointSlice(obj *apisv1alpha1.APIExportEndpointSlice, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info(fmt.Sprintf("queueing APIExportEndpointSlice%s", logSuffix))
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}
	obj, err := c.getAPIExportEndpointSlice(clusterName.Path(), name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, obj)
	if err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	globalAPIExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportEndpointSliceByAPIExport: indexers.IndexAPIExportEndpointSliceByAPIExport,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportEndpointSliceByAPIExport: indexers.IndexAPIExportEndpointSliceByAPIExport,
	})
	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})
}

func objOrTombstone[T runtime.Object](obj any) T {
	if t, ok := obj.(T); ok {
		return t
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if t, ok := tombstone.Obj.(T); ok {
			return t
		}

		panic(fmt.Errorf("tombstone %T is not a %T", tombstone, new(T)))
	}

	panic(fmt.Errorf("%T is not a %T", obj, new(T)))
}
