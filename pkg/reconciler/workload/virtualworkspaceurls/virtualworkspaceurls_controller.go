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

package virtualworkspaceurls

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	v1alpha12 "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const controllerName = "kcp-workloadcluster-controller"

func NewController(
	kcpClusterClient kcpclient.ClusterInterface,
	workloadClusterInformer v1alpha1.WorkloadClusterInformer,
	workspaceShardInformer workspaceinformer.ClusterWorkspaceShardInformer,
) *Controller {

	c := &Controller{
		queue:                  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		kcpClusterClient:       kcpClusterClient,
		workloadClusterIndexer: workloadClusterInformer.Informer().GetIndexer(),
		workspaceShardLister:   workspaceShardInformer.Lister(),
	}

	// Watch for events related to WorkloadClusters
	workloadClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkloadCluster(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkloadCluster(obj) },
		DeleteFunc: func(obj interface{}) {},
	})

	// Watch for events related to workspaceShards
	workspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
	})

	return c
}

type Controller struct {
	queue            workqueue.RateLimitingInterface
	kcpClusterClient kcpclient.ClusterInterface

	workspaceShardLister   v1alpha12.ClusterWorkspaceShardLister
	workloadClusterIndexer cache.Indexer
}

func (c *Controller) enqueueWorkloadCluster(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

// On workspaceShard changes, enqueue all the workloadClusters.
func (c *Controller) enqueueWorkspaceShard(obj interface{}) {
	for _, workloadCluster := range c.workloadClusterIndexer.List() {
		key, err := cache.MetaNamespaceKeyFunc(workloadCluster)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		c.queue.Add(key)
	}
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting workers", "controller", controllerName)
	defer klog.InfoS("Stopping workers", "controller", controllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	obj, exists, err := c.workloadClusterIndexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to get workloadCluster with key %q because: %v", key, err)
		return nil
	}

	if !exists {
		klog.Infof("workloadCluster with key %q was deleted", key)
		return nil
	}

	klog.Infof("Processing workloadCluster %q", key)
	workspacesShards, err := c.workspaceShardLister.List(labels.Everything())
	if err != nil {
		return err
	}

	currentWorkloadCluster := obj.(*workloadv1alpha1.WorkloadCluster)
	newWorkloadCluster, err := c.reconcile(currentWorkloadCluster, workspacesShards)
	if err != nil {
		klog.Errorf("Failed to reconcile workloadCluster %q because: %v", key, err)
		return err
	}

	if reflect.DeepEqual(currentWorkloadCluster, newWorkloadCluster) {
		return nil
	}

	currentWorkloadClusterJSON, err := json.Marshal(currentWorkloadCluster)
	if err != nil {
		klog.Errorf("Failed to marshal workloadCluster %q because: %v", key, err)
		return err
	}
	newWorkloadClusterJSON, err := json.Marshal(newWorkloadCluster)
	if err != nil {
		klog.Errorf("Failed to marshal workloadCluster %q because: %v", key, err)
		return err
	}

	patchBytes, err := jsonpatch.CreateMergePatch(currentWorkloadClusterJSON, newWorkloadClusterJSON)
	if err != nil {
		klog.Errorf("Failed to create merge patch for workloadCluster %q because: %v", key, err)
		return err
	}

	if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(currentWorkloadCluster)).WorkloadV1alpha1().WorkloadClusters().Patch(ctx, currentWorkloadCluster.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status"); err != nil {
		klog.Errorf("failed to patch workload cluster status: %v", err)
		return err
	}
	klog.V(2).InfoS("updated workload cluster status", "WorkloadCluster", newWorkloadCluster.Name, "LogicalCluster", logicalcluster.From(newWorkloadCluster))

	return nil
}
