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

package apiexportendpointslice

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
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
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/apis/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	topologyinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/topology/v1alpha1"
)

const (
	ControllerName = "kcp-apiexportendpointslice"
)

// NewController returns a new controller for APIExportEndpointSlices.
// Shards and APIExports are read from the cache server.
func NewController(
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	globalAPIExportClusterInformer apisv1alpha1informers.APIExportClusterInformer,
	partitionClusterInformer topologyinformers.PartitionClusterInformer,
	kcpClusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		listAPIExportEndpointSlices: func() ([]*apisv1alpha1.APIExportEndpointSlice, error) {
			return apiExportEndpointSliceClusterInformer.Lister().List(labels.Everything())
		},
		getAPIExportEndpointSlice: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error) {
			return indexers.ByPathAndName[*apisv1alpha1.APIExportEndpointSlice](apisv1alpha1.Resource("apiexportendpointslices"), apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), path, name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportClusterInformer.Informer().GetIndexer(), path, name)
		},
		getPartition: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error) {
			return partitionClusterInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportEndpointSlicesByPartition: func(key string) ([]*apisv1alpha1.APIExportEndpointSlice, error) {
			list, err := apiExportEndpointSliceClusterInformer.Informer().GetIndexer().ByIndex(indexAPIExportEndpointSlicesByPartition, key)
			if err != nil {
				return nil, err
			}
			var slices []*apisv1alpha1.APIExportEndpointSlice
			for _, obj := range list {
				slice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
				if !ok {
					return nil, fmt.Errorf("obj is supposed to be an APIExportEndpointSlice, but is %T", obj)
				}
				slices = append(slices, slice)
			}
			return slices, nil
		},
		apiExportEndpointSliceClusterInformer: apiExportEndpointSliceClusterInformer,
		commit:                                committer.NewCommitter[*APIExportEndpointSlice, Patcher, *APIExportEndpointSliceSpec, *APIExportEndpointSliceStatus](kcpClusterClient.ApisV1alpha1().APIExportEndpointSlices()),
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

	_, _ = globalAPIExportClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(objOrTombstone[*apisv1alpha1.APIExport](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(objOrTombstone[*apisv1alpha1.APIExport](obj), logger)
		},
	}))

	_, _ = partitionClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueuePartition(objOrTombstone[*topologyv1alpha1.Partition](obj), logger)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueuePartition(objOrTombstone[*topologyv1alpha1.Partition](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueuePartition(objOrTombstone[*topologyv1alpha1.Partition](obj), logger)
		},
	}))

	return c, nil
}

type APIExportEndpointSlice = apisv1alpha1.APIExportEndpointSlice
type APIExportEndpointSliceSpec = apisv1alpha1.APIExportEndpointSliceSpec
type APIExportEndpointSliceStatus = apisv1alpha1.APIExportEndpointSliceStatus
type Patcher = apisv1alpha1client.APIExportEndpointSliceInterface
type Resource = committer.Resource[*APIExportEndpointSliceSpec, *APIExportEndpointSliceStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIExportEndpointSlices. It ensures that the shard endpoints are populated
// in the status of every APIExportEndpointSlices.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	listAPIExportEndpointSlices           func() ([]*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExportEndpointSlice             func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExport                          func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getPartition                          func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
	getAPIExportEndpointSlicesByPartition func(key string) ([]*apisv1alpha1.APIExportEndpointSlice, error)

	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer
	commit                                CommitFunc
}

// enqueueAPIExportEndpointSlice enqueues an APIExportEndpointSlice.
func (c *controller) enqueueAPIExportEndpointSlice(slice *apisv1alpha1.APIExportEndpointSlice, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(slice)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info(fmt.Sprintf("queueing APIExportEndpointSlice%s", logSuffix))
	c.queue.Add(key)
}

// enqueueAPIExportEndpointSlicesForAPIExport enqueues APIExportEndpointSlices referencing a specific APIExport.
func (c *controller) enqueueAPIExportEndpointSlicesForAPIExport(export *apisv1alpha1.APIExport, logger logr.Logger) {
	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, path.Join(export.Name).String())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexers.APIExportEndpointSliceByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range sets.List[string](keys) {
		slice, exists, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		} else if !exists {
			continue
		}
		c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](slice), logger, " because of referenced APIExport")
	}
}

// enqueuePartition maps a Partition to APIExportEndpointSlices for enqueuing.
func (c *controller) enqueuePartition(obj *topologyv1alpha1.Partition, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	slices, err := c.getAPIExportEndpointSlicesByPartition(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, slice := range slices {
		c.enqueueAPIExportEndpointSlice(objOrTombstone[*apisv1alpha1.APIExportEndpointSlice](slice), logger, " because of Partition change")
	}
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

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}
	obj, err := c.getAPIExportEndpointSlice(clusterName.Path(), name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}

	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	globalAPIExportClusterInformer apisv1alpha1informers.APIExportClusterInformer,
	apiExportEndpointSliceClusterInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(globalAPIExportClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportEndpointSliceByAPIExport: indexers.IndexAPIExportEndpointSliceByAPIExport,
	})
	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSlicesByPartition: indexAPIExportEndpointSlicesByPartitionFunc,
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
