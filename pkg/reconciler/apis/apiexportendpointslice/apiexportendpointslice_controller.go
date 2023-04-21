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
	"reflect"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/apis/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	topologyinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/topology/v1alpha1"
)

const (
	ControllerName = "kcp-apiexportendpointslice"
)

// NewController returns a new controller for APIExportEndpointSlices.
// Shards and APIExports are read from the cache server.
func NewController(
	apiExportEndpointSliceClusterInformer apisinformers.APIExportEndpointSliceClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
	globalAPIExportClusterInformer apisinformers.APIExportClusterInformer,
	partitionClusterInformer topologyinformers.PartitionClusterInformer,
	kcpClusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		listAPIExportEndpointSlices: func() ([]*apisv1alpha1.APIExportEndpointSlice, error) {
			return apiExportEndpointSliceClusterInformer.Lister().List(labels.Everything())
		},
		listShards: func(selector labels.Selector) ([]*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Lister().List(selector)
		},
		getAPIExportEndpointSlice: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExportEndpointSlice, error) {
			return apiExportEndpointSliceClusterInformer.Lister().Cluster(clusterName).Get(name)
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

	indexers.AddIfNotPresentOrDie(globalAPIExportClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSliceByAPIExport: indexAPIExportEndpointSliceByAPIExportFunc,
	})

	_, _ = apiExportEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExportEndpointSlice(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlice(obj)
		},
	})

	_, _ = globalAPIExportClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExportEndpointSlicesForAPIExport(obj)
		},
	})

	_, _ = globalShardClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAllAPIExportEndpointSlices(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if filterShardEvent(oldObj, newObj) {
				c.enqueueAllAPIExportEndpointSlices(newObj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAllAPIExportEndpointSlices(obj)
		},
	},
	)

	_, _ = partitionClusterInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueuePartition(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueuePartition(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueuePartition(obj)
			},
		},
	)

	indexers.AddIfNotPresentOrDie(apiExportEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSlicesByPartition: indexAPIExportEndpointSlicesByPartitionFunc,
	})

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
	queue workqueue.RateLimitingInterface

	listShards                            func(selector labels.Selector) ([]*corev1alpha1.Shard, error)
	listAPIExportEndpointSlices           func() ([]*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExportEndpointSlice             func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExportEndpointSlice, error)
	getAPIExport                          func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getPartition                          func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
	getAPIExportEndpointSlicesByPartition func(key string) ([]*apisv1alpha1.APIExportEndpointSlice, error)

	apiExportEndpointSliceClusterInformer apisinformers.APIExportEndpointSliceClusterInformer
	commit                                CommitFunc
}

// enqueueAPIExportEndpointSlice enqueues an APIExportEndpointSlice.
func (c *controller) enqueueAPIExportEndpointSlice(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing APIExportEndpointSlice")
	c.queue.Add(key)
}

// enqueueAPIExportEndpointSlicesForAPIExport enqueues APIExportEndpointSlices referencing a specific APIExport.
func (c *controller) enqueueAPIExportEndpointSlicesForAPIExport(obj interface{}) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	export, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIExport, but is %T", obj))
		return
	}

	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, path.Join(export.Name).String())
		if err != nil {
			runtime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(indexAPIExportEndpointSliceByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range sets.List[string](keys) {
		slice, exists, err := c.apiExportEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			runtime.HandleError(err)
			continue
		} else if !exists {
			continue
		}
		logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*apisv1alpha1.APIExport))
		logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlices because of referenced APIExport")
		c.enqueueAPIExportEndpointSlice(slice)
	}
}

// enqueueAllAPIExportEndpointSlices enqueues all APIExportEndpointSlices.
func (c *controller) enqueueAllAPIExportEndpointSlices(shard interface{}) {
	list, err := c.listAPIExportEndpointSlices()
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), shard.(*corev1alpha1.Shard))
	for i := range list {
		key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(list[i])
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlice because Shard changed")
		c.queue.Add(key)
	}
}

// enqueuePartition maps a Partition to APIExportEndpointSlices for enqueuing.
func (c *controller) enqueuePartition(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	slices, err := c.getAPIExportEndpointSlicesByPartition(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*topologyv1alpha1.Partition))
	logging.WithQueueKey(logger, key).V(4).Info("queuing APIExportEndpointSlices because Partition changed")
	for _, slice := range slices {
		c.enqueueAPIExportEndpointSlice(slice)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
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
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	obj, err := c.getAPIExportEndpointSlice(clusterName, name)
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

// filterShardEvent returns true if the event passes the filter and needs to be processed false otherwise.
func filterShardEvent(oldObj, newObj interface{}) bool {
	oldShard, ok := oldObj.(*corev1alpha1.Shard)
	if !ok {
		return false
	}
	newShard, ok := newObj.(*corev1alpha1.Shard)
	if !ok {
		return false
	}
	if oldShard.Spec.VirtualWorkspaceURL != newShard.Spec.VirtualWorkspaceURL {
		return true
	}
	if !reflect.DeepEqual(oldShard.Labels, newShard.Labels) {
		return true
	}
	return false
}
