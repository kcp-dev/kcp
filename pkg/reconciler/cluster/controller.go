/*
Copyright 2021 The KCP Authors.

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

package cluster

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	clusterinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
)

const GVRForLocationInLogicalClusterIndexName = "GVRForLocationInLogicalCluster"

func GetGVRForLocationInLogicalClusterIndexKey(location, clusterName string, gvr metav1.GroupVersionResource) string {
	return location + "$$" + apiresource.GetClusterNameAndGVRIndexKey(clusterName, gvr)
}

const LocationInLogicalClusterIndexName = "LocationInLogicalCluster"

func GetLocationInLogicalClusterIndexKey(location, clusterName string) string {
	return location + "/" + clusterName
}

// ClusterReconcileImpl defines the methods that ClusterReconciler
// will call in response to changes to Cluster resources.
type ClusterReconcileImpl interface {
	Reconcile(ctx context.Context, cluster *clusterv1alpha1.Cluster) error
	Cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster)
}

// NewClusterReconciler returns a new controller which reconciles
// Cluster resources in the API server it reaches using the REST
// client.
func NewClusterReconciler(
	name string,
	reconciler ClusterReconcileImpl,
	kcpClient kcpclient.Interface,
	kcpClusterClient *kcpclient.Cluster,
	clusterInformer clusterinformer.ClusterInformer,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
) (*ClusterReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name)

	c := &ClusterReconciler{
		name:                     name,
		reconciler:               reconciler,
		kcpClient:                kcpClient,
		kcpClusterClient:         kcpClusterClient,
		clusterIndexer:           clusterInformer.Informer().GetIndexer(),
		apiresourceImportIndexer: apiResourceImportInformer.Informer().GetIndexer(),
		queue:                    queue,
	}

	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.deletedCluster(obj) },
	})
	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIResourceImportRelatedCluster(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceImportRelatedCluster(obj)
		},
	})

	indexers := map[string]cache.IndexFunc{
		GVRForLocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetGVRForLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, apiResourceImport.ClusterName, apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
		LocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, apiResourceImport.ClusterName)}, nil
			}
			return []string{}, nil
		},
	}

	// Ensure the indexers are only added if not already present.
	for indexName := range c.apiresourceImportIndexer.GetIndexers() {
		delete(indexers, indexName)
	}
	if len(indexers) > 0 {
		if err := c.apiresourceImportIndexer.AddIndexers(indexers); err != nil {
			return nil, fmt.Errorf("Failed to add indexer for APIResourceImport: %w", err)
		}
	}

	return c, nil
}

type ClusterReconciler struct {
	name                     string
	reconciler               ClusterReconcileImpl
	kcpClient                kcpclient.Interface // TODO(sttts): get rid of this. Doesn't make sense. Use kcpClusterClient.
	kcpClusterClient         *kcpclient.Cluster
	clusterIndexer           cache.Indexer
	apiresourceImportIndexer cache.Indexer

	queue workqueue.RateLimitingInterface
}

func (c *ClusterReconciler) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *ClusterReconciler) enqueueAPIResourceImportRelatedCluster(obj interface{}) {
	var apiResourceImport *apiresourcev1alpha1.APIResourceImport
	switch typedObj := obj.(type) {
	case *apiresourcev1alpha1.APIResourceImport:
		apiResourceImport = typedObj
	case cache.DeletedFinalStateUnknown:
		deletedImport, ok := typedObj.Obj.(*apiresourcev1alpha1.APIResourceImport)
		if ok {
			apiResourceImport = deletedImport
		}
	}
	if apiResourceImport != nil {
		c.enqueue(&metav1.PartialObjectMetadata{
			ObjectMeta: metav1.ObjectMeta{
				Name:        apiResourceImport.Spec.Location,
				ClusterName: apiResourceImport.ClusterName,
			},
		})
	}
}

func (c *ClusterReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *ClusterReconciler) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", c.name)
	defer klog.Infof("Shutting down %s controller", c.name)

	wait.Until(func() { c.startWorker(ctx) }, time.Millisecond*10, ctx.Done())
}

func (c *ClusterReconciler) ShutDown() {
	c.queue.ShutDown()
}

func (c *ClusterReconciler) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", c.name, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *ClusterReconciler) process(ctx context.Context, key string) error {
	obj, exists, err := c.clusterIndexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		klog.Errorf("%s: Object with key %q was deleted", c.name, key)
		return nil
	}
	current := obj.(*clusterv1alpha1.Cluster).DeepCopy()
	previous := current.DeepCopy()

	if err := c.reconciler.Reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, current.Status) {
		_, uerr := c.kcpClusterClient.Cluster(current.ClusterName).ClusterV1alpha1().Clusters().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return uerr
	}

	return nil
}

func (c *ClusterReconciler) deletedCluster(obj interface{}) {
	castObj, ok := obj.(*clusterv1alpha1.Cluster)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("%s: Couldn't get object from tombstone %#v", c.name, obj)
			return
		}
		castObj, ok = tombstone.Obj.(*clusterv1alpha1.Cluster)
		if !ok {
			klog.Errorf("%s: Tombstone contained object that is not expected %#v", c.name, obj)
			return
		}
	}
	klog.V(4).Infof("%s: Responding to deletion of cluster %q", c.name, castObj.Name)
	ctx := context.TODO()
	c.reconciler.Cleanup(ctx, castObj)
}
