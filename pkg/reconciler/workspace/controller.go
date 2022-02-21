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

package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyhelper "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	currentShardIndex  = "shard"
	unschedulableIndex = "unschedulable"
	controllerName     = "workspace"
)

func NewController(
	kcpClient kcpclient.ClusterInterface,
	workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	workspaceShardInformer tenancyinformer.WorkspaceShardInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-workspace")

	c := &Controller{
		queue:                 queue,
		kcpClient:             kcpClient,
		workspaceIndexer:      workspaceInformer.Informer().GetIndexer(),
		workspaceLister:       workspaceInformer.Lister(),
		workspaceShardIndexer: workspaceShardInformer.Informer().GetIndexer(),
		workspaceShardLister:  workspaceShardInformer.Lister(),
		syncChecks: []cache.InformerSynced{
			workspaceInformer.Informer().HasSynced,
			workspaceShardInformer.Informer().HasSynced,
		},
	}

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	if err := c.workspaceIndexer.AddIndexers(map[string]cache.IndexFunc{
		currentShardIndex: func(obj interface{}) ([]string, error) {
			if workspace, ok := obj.(*tenancyv1alpha1.ClusterWorkspace); ok {
				return []string{workspace.Status.Location.Current}, nil
			}
			return []string{}, nil
		},
		unschedulableIndex: func(obj interface{}) ([]string, error) {
			if workspace, ok := obj.(*tenancyv1alpha1.ClusterWorkspace); ok {
				if conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && conditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable {
					return []string{"true"}, nil
				}
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for ClusterWorkspace: %w", err)
	}

	workspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAddedShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueDeletedShard(obj) },
	})

	return c, nil
}

// Controller watches Workspaces and WorkspaceShards in order to make sure every ClusterWorkspace
// is scheduled to a valid WorkspaceShard.
type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClient        kcpclient.ClusterInterface
	workspaceIndexer cache.Indexer
	workspaceLister  tenancylister.ClusterWorkspaceLister

	workspaceShardIndexer cache.Indexer
	workspaceShardLister  tenancylister.WorkspaceShardLister

	syncChecks []cache.InformerSynced
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.Infof("queueing workspace %q", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueAddedShard(obj interface{}) {
	shard, ok := obj.(*tenancyv1alpha1.WorkspaceShard)
	if !ok {
		runtime.HandleError(fmt.Errorf("got %T when handling added WorkspaceShard", obj))
		return
	}
	klog.Infof("handling added shard %q", shard.Name)
	workspaces, err := c.workspaceIndexer.ByIndex(unschedulableIndex, "true")
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, workspace := range workspaces {
		key, err := cache.MetaNamespaceKeyFunc(workspace)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		klog.Infof("queuing unschedulable workspace %q", key)
		c.queue.Add(key)
	}
}

func (c *Controller) enqueueDeletedShard(obj interface{}) {
	shard, ok := obj.(*tenancyv1alpha1.WorkspaceShard)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.V(2).Infof("Couldn't get object from tombstone %#v", obj)
			return
		}
		shard, ok = tombstone.Obj.(*tenancyv1alpha1.WorkspaceShard)
		if !ok {
			klog.V(2).Infof("Tombstone contained object that is not a WorkspaceShard: %#v", obj)
			return
		}
	}
	klog.Infof("handling removed shard %q", shard.Name)
	workspaces, err := c.workspaceIndexer.ByIndex(currentShardIndex, shard.Name)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, workspace := range workspaces {
		key, err := cache.MetaNamespaceKeyFunc(workspace)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		klog.Infof("queuing orphaned workspace %q", key)
		c.queue.Add(key)
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("Starting ClusterWorkspace controller")
	defer klog.Info("Shutting down ClusterWorkspace controller")

	if !cache.WaitForNamedCacheSync(controllerName, ctx.Done(), c.syncChecks...) {
		klog.Warning("Failed to wait for caches to sync")
		return
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
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

	klog.Infof("processing key %q", key)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	namespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	if namespace != "" {
		klog.Errorf("namespace %q found in key for cluster-wide ClusterWorkspace object", namespace)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.workspaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	previous := obj
	obj = obj.DeepCopy()

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, obj.Status) {
		oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			Status: previous.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace %q|%q/%q: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             previous.UID,
				ResourceVersion: previous.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace %q|%q/%q: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace %q|%q/%q: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}

func (c *Controller) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	var shard *tenancyv1alpha1.WorkspaceShard
	if currentShardName := workspace.Status.Location.Current; currentShardName != "" {
		// make sure current shard still exists
		currentShard, err := c.workspaceShardLister.Get(clusters.ToClusterAwareKey(workspace.ClusterName, currentShardName))
		if errors.IsNotFound(err) {
			klog.Infof("de-scheduling workspace %q|%q from nonexistent shard %q", workspace.ClusterName, workspace.Name, currentShardName)
			workspace.Status.Location.Current = ""
		} else if err != nil {
			return err
		}
		shard = currentShard
	}
	if workspace.Status.Location.Current == "" {
		// find a shard for this workspace
		shards, err := c.workspaceShardLister.List(labels.Everything())
		if err != nil {
			return err
		}
		// TODO: do we need to handle cross-cluster shard assignment? if so, we need shard name for the shard objects...
		var filtered []*tenancyv1alpha1.WorkspaceShard
		for i := range shards {
			if shards[i].ClusterName == workspace.ClusterName {
				filtered = append(filtered, shards[i])
			}
		}
		if len(filtered) != 0 {
			targetShard := filtered[rand.Intn(len(filtered))]
			workspace.Status.Location.Target = targetShard.Name
			shard = targetShard
			klog.Infof("scheduling workspace %q|%q to %q|%q", workspace.ClusterName, workspace.Name, targetShard.ClusterName, targetShard.Name)
		}
	}
	if workspace.Status.Location.Target != "" && workspace.Status.Location.Current != workspace.Status.Location.Target {
		klog.Infof("moving workspace %q to %q", workspace.Name, workspace.Status.Location.Target)
		workspace.Status.Location.Current = workspace.Status.Location.Target
		workspace.Status.Location.Target = ""
		// TODO: actually handle the RV resolution and double-sided accept we need for movement
		if len(workspace.Status.Location.History) == 0 {
			workspace.Status.Location.History = []tenancyv1alpha1.ShardStatus{
				{Name: workspace.Status.Location.Current, LiveAfterResourceVersion: "1"},
			}
		} else {
			previous := workspace.Status.Location.History[len(workspace.Status.Location.History)-1]
			startingRV, err := strconv.ParseInt(previous.LiveAfterResourceVersion, 10, 64)
			if err != nil {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "Invalid status.location.history[%d].liveAfterResourceVersion: %v.", len(workspace.Status.Location.History)-1, err)
				return nil
			}
			endingRV := strconv.FormatInt(startingRV+10, 10)
			previous.LiveBeforeResourceVersion = endingRV
			current := tenancyv1alpha1.ShardStatus{
				Name:                     workspace.Status.Location.Current,
				LiveAfterResourceVersion: endingRV,
			}
			workspace.Status.Location.History[len(workspace.Status.Location.History)-1] = previous
			workspace.Status.Location.History = append(workspace.Status.Location.History, current)
		}
	}
	if workspace.Status.Location.Current == "" {
		workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseInitializing
		conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "No shards are available to schedule Workspaces to.")
	} else {
		workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseActive
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
	}
	// expose the correct base URL given our current shard
	if shard == nil || !conditions.IsTrue(shard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
		conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceURLValid, tenancyv1alpha1.WorkspaceURLReasonMissing, conditionsv1alpha1.ConditionSeverityError, "No connection information on target WorkspaceShard.")
	} else {
		u, err := url.Parse(shard.Status.ConnectionInfo.Host)
		if err != nil {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceURLValid, tenancyv1alpha1.WorkspaceURLReasonInvalid, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target WorkspaceShard: %v.", err)
			return nil
		}
		logicalCluster, err := tenancyhelper.EncodeLogicalClusterName(workspace)
		if err != nil {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceURLValid, tenancyv1alpha1.WorkspaceURLReasonInvalid, conditionsv1alpha1.ConditionSeverityError, "Invalid ClusterWorkspace location: %v.", err)
			return nil
		}
		u.Path = path.Join(u.Path, shard.Status.ConnectionInfo.APIPath, "clusters", logicalCluster)
		workspace.Status.BaseURL = u.String()
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceURLValid)
	}
	return nil
}
