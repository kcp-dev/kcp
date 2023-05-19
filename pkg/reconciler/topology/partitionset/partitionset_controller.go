/*
Copyright 2023 The KCP Authors.

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

package partitionset

import (
	"context"
	"fmt"
	"reflect"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	topologyv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/topology/v1alpha1"
	coreinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	topologyinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/topology/v1alpha1"
)

const (
	ControllerName = "kcp-topology-partitionset"
)

// NewController returns a new controller for PartitionSets.
func NewController(
	partitionSetClusterInformer topologyinformers.PartitionSetClusterInformer,
	partitionClusterInformer topologyinformers.PartitionClusterInformer,
	globalShardClusterInformer coreinformers.ShardClusterInformer,
	kcpClusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue:            queue,
		kcpClusterClient: kcpClusterClient,
		listShards: func(selector labels.Selector) ([]*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Lister().List(selector)
		},
		listPartitionSets: func() ([]*topologyv1alpha1.PartitionSet, error) {
			return partitionSetClusterInformer.Lister().List(labels.Everything())
		},
		getPartitionSet: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.PartitionSet, error) {
			return partitionSetClusterInformer.Lister().Cluster(clusterName).Get(name)
		},
		// getPartitionsByPartitionSet calls the API directly instead of using an indexer
		// as otherwise duplicates are created when the cache is not updated quickly enough.
		getPartitionsByPartitionSet: func(ctx context.Context, partitionSet *topologyv1alpha1.PartitionSet) ([]*topologyv1alpha1.Partition, error) {
			partitions, err := kcpClusterClient.Cluster(logicalcluster.From(partitionSet).Path()).TopologyV1alpha1().Partitions().List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			result := []*topologyv1alpha1.Partition{}
			for i := range partitions.Items {
				for _, owner := range partitions.Items[i].OwnerReferences {
					ownerGV, err := schema.ParseGroupVersion(owner.APIVersion)
					if err != nil {
						continue
					}
					if owner.UID == partitionSet.UID &&
						owner.Kind == "PartitionSet" &&
						ownerGV.Group == topologyv1alpha1.SchemeGroupVersion.Group {
						result = append(result, &partitions.Items[i])
						break
					}
				}
			}
			return result, nil
		},
		createPartition: func(ctx context.Context, path logicalcluster.Path, partition *topologyv1alpha1.Partition) (*topologyv1alpha1.Partition, error) {
			return kcpClusterClient.Cluster(path).TopologyV1alpha1().Partitions().Create(ctx, partition, metav1.CreateOptions{})
		},
		deletePartition: func(ctx context.Context, path logicalcluster.Path, partitionName string) error {
			return kcpClusterClient.Cluster(path).TopologyV1alpha1().Partitions().Delete(ctx, partitionName, metav1.DeleteOptions{})
		},

		commit: committer.NewCommitter[*PartitionSet, Patcher, *PartitionSetSpec, *PartitionSetStatus](kcpClusterClient.TopologyV1alpha1().PartitionSets()),
	}

	_, _ = globalShardClusterInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueAllPartitionSets(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				// limit reconciliation of all PartitionSets (costly) to shard changes impacting partitioning
				if filterShardEvent(oldObj, newObj) {
					c.enqueueAllPartitionSets(newObj)
				}
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueAllPartitionSets(obj)
			},
		},
	)

	_, _ = partitionSetClusterInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueuePartitionSet(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueuePartitionSet(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueuePartitionSet(obj)
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

	return c, nil
}

type PartitionSet = topologyv1alpha1.PartitionSet
type PartitionSetSpec = topologyv1alpha1.PartitionSetSpec
type PartitionSetStatus = topologyv1alpha1.PartitionSetStatus
type Patcher = topologyv1alpha1client.PartitionSetInterface
type Resource = committer.Resource[*PartitionSetSpec, *PartitionSetStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles PartitionSets. It ensures that Partitions are aligned
// with the PartitionSets specifications and theShards.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	listShards                  func(selector labels.Selector) ([]*corev1alpha1.Shard, error)
	listPartitionSets           func() ([]*topologyv1alpha1.PartitionSet, error)
	getPartitionSet             func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.PartitionSet, error)
	getPartitionsByPartitionSet func(ctx context.Context, partitionSet *topologyv1alpha1.PartitionSet) ([]*topologyv1alpha1.Partition, error)
	createPartition             func(ctx context.Context, path logicalcluster.Path, partition *topologyv1alpha1.Partition) (*topologyv1alpha1.Partition, error)
	deletePartition             func(ctx context.Context, path logicalcluster.Path, partitionName string) error
	commit                      CommitFunc
}

// enqueuePartitionSet enqueues a PartitionSet.
func (c *controller) enqueuePartitionSet(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing PartitionSet")
	c.queue.Add(key)
}

// enqueueAllPartitionSets enqueues all PartitionSets.
func (c *controller) enqueueAllPartitionSets(shard interface{}) {
	list, err := c.listPartitionSets()
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), shard.(*corev1alpha1.Shard))
	for i := range list {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(list[i])
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(4).Info("queuing PartitionSet because Shard changed")
		c.queue.Add(key)
	}
}

// enqueuePartition maps a Partition to a PartitionSet for enqueuing.
func (c *controller) enqueuePartition(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	partition, ok := obj.(*topologyv1alpha1.Partition)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a Partition, but is %T", obj))
		return
	}
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*topologyv1alpha1.Partition))

	var partitionSet *topologyv1alpha1.PartitionSet
	for _, ownerRef := range partition.OwnerReferences {
		if ownerRef.Kind != "PartitionSet" {
			continue
		}
		partitionSet, err = c.getPartitionSet(logicalcluster.From(partition), ownerRef.Name)
		if err != nil {
			if errors.IsNotFound(err) {
				return // object deleted before we handled it
			}
			runtime.HandleError(err)
			return
		}
	}
	if partitionSet == nil {
		logging.WithQueueKey(logger, key).V(6).Info("No PartitionSet for this Partition")
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info("queuing PartitionSet because Partition changed")
	c.enqueuePartitionSet(partitionSet)
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
	obj, err := c.getPartitionSet(clusterName, name)
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
	return !reflect.DeepEqual(oldShard.Labels, newShard.Labels)
}
