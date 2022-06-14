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

package clusterworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	currentShardIndex  = "shard"
	unschedulableIndex = "unschedulable"
	controllerName     = "workspace"
)

func NewController(
	kcpClient kcpclient.ClusterInterface,
	workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	rootWorkspaceShardInformer tenancyinformer.ClusterWorkspaceShardInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &Controller{
		queue:                     queue,
		kcpClient:                 kcpClient,
		workspaceIndexer:          workspaceInformer.Informer().GetIndexer(),
		workspaceLister:           workspaceInformer.Lister(),
		rootWorkspaceShardIndexer: rootWorkspaceShardInformer.Informer().GetIndexer(),
		rootWorkspaceShardLister:  rootWorkspaceShardInformer.Lister(),
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

	rootWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueUpsertedShard(obj, "add") },
		UpdateFunc: func(obj, _ interface{}) { c.enqueueUpsertedShard(obj, "update") },
		DeleteFunc: func(obj interface{}) { c.enqueueDeletedShard(obj) },
	})

	return c, nil
}

// Controller watches Workspaces and WorkspaceShards in order to make sure every ClusterWorkspace
// is scheduled to a valid ClusterWorkspaceShard.
type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClient        kcpclient.ClusterInterface
	workspaceIndexer cache.Indexer
	workspaceLister  tenancylister.ClusterWorkspaceLister

	rootWorkspaceShardIndexer cache.Indexer
	rootWorkspaceShardLister  tenancylister.ClusterWorkspaceShardLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.Infof("Queueing workspace %q", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueUpsertedShard(obj interface{}, verb string) {
	shard, ok := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
	if !ok {
		runtime.HandleError(fmt.Errorf("got %T when handling added ClusterWorkspaceShard", obj))
		return
	}
	klog.Infof("Handling %sed shard %q", verb, shard.Name)
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
		klog.Infof("Queuing unschedulable workspace %q", key)
		c.queue.Add(key)
	}
}

func (c *Controller) enqueueDeletedShard(obj interface{}) {
	shard, ok := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.V(2).Infof("Couldn't get object from tombstone %#v", obj)
			return
		}
		shard, ok = tombstone.Obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
		if !ok {
			klog.V(2).Infof("Tombstone contained object that is not a ClusterWorkspaceShard: %#v", obj)
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
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
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
	previous := obj.DeepCopy()
	obj = obj.DeepCopy()

	reconcileMetadata(obj)

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.ObjectMeta, obj.ObjectMeta) {
		// to ensure they appear in the patch as preconditions
		previous.ObjectMeta.UID = ""
		previous.ObjectMeta.ResourceVersion = ""
		oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: previous.ObjectMeta,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: obj.ObjectMeta,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace %s|%s: %w", clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace %s|%s: %w", clusterName, name, err)
		}
		_, uerr := c.kcpClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		return uerr
	}

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, obj.Status) {
		oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			Status: previous.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             previous.UID,
				ResourceVersion: previous.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace %s|%s: %w", clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace %s|%s: %w", clusterName, name, err)
		}
		_, uerr := c.kcpClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}

func reconcileMetadata(workspace *tenancyv1alpha1.ClusterWorkspace) {
	if workspace.Labels == nil {
		workspace.Labels = map[string]string{}
	}
	workspace.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel] = string(workspace.Status.Phase)

	initializerKeys := sets.NewString()
	for _, initializer := range workspace.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		initializerKeys.Insert(key)
		workspace.Labels[key] = value
	}

	for key := range workspace.Labels {
		if strings.HasPrefix(key, tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix) {
			if !initializerKeys.Has(key) {
				delete(workspace.Labels, key)
			}
		}
	}
}

func (c *Controller) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	workspaceClusterName := logicalcluster.From(workspace)
	switch workspace.Status.Phase {
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		// possibly de-schedule while still in scheduling phase
		if current := workspace.Status.Location.Current; current != "" {
			// make sure current shard still exists
			if shard, err := c.rootWorkspaceShardLister.Get(clusters.ToClusterAwareKey(tenancyv1alpha1.RootCluster, current)); errors.IsNotFound(err) {
				klog.Infof("De-scheduling workspace %s|%s from nonexistent shard %q", tenancyv1alpha1.RootCluster, workspace.Name, current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			} else if err != nil {
				return err
			} else if valid, _, _ := isValidShard(shard); !valid {
				klog.Infof("De-scheduling workspace %s|%s from invalid shard %q", tenancyv1alpha1.RootCluster, workspace.Name, current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			}
		}

		if workspace.Status.Location.Current == "" {
			// find a shard for this workspace, randomly
			shards, err := c.rootWorkspaceShardLister.List(labels.Everything())
			if err != nil {
				return err
			}

			validShards := make([]*tenancyv1alpha1.ClusterWorkspaceShard, 0, len(shards))
			invalidShards := map[string]struct {
				reason, message string
			}{}
			for _, shard := range shards {
				if valid, reason, message := isValidShard(shard); valid {
					validShards = append(validShards, shard)
				} else {
					invalidShards[shard.Name] = struct {
						reason, message string
					}{
						reason:  reason,
						message: message,
					}
				}
			}

			if len(validShards) > 0 {
				targetShard := validShards[rand.Intn(len(validShards))]

				u, err := url.Parse(targetShard.Spec.ExternalURL)
				if err != nil {
					// shouldn't happen since we just checked in isValidShard
					conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonReasonUnknown, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target ClusterWorkspaceShard: %v.", err)
					return err // requeue
				}
				u.Path = path.Join(u.Path, workspaceClusterName.Join(workspace.Name).Path())

				workspace.Status.BaseURL = u.String()
				workspace.Status.Location.Current = targetShard.Name

				conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				klog.Infof("Scheduled workspace %s|%s to %s|%s", workspaceClusterName, workspace.Name, logicalcluster.From(targetShard), targetShard.Name)
			} else {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "No available shards to schedule the workspace.")
				failures := make([]string, 0, len(invalidShards))
				for name, x := range invalidShards {
					failures = append(failures, fmt.Sprintf("  %s: reason %q, message %q", name, x.reason, x.message))
				}
				klog.Infof("No valid shards found for workspace %s|%s, skipped:\n%s", workspaceClusterName, workspace.Name, strings.Join(failures, "\n"))
			}
		}

	case tenancyv1alpha1.ClusterWorkspacePhaseInitializing, tenancyv1alpha1.ClusterWorkspacePhaseReady:
		// movement can only happen after scheduling
		if workspace.Status.Location.Target == "" {
			break
		}

		current, target := workspace.Status.Location.Current, workspace.Status.Location.Target
		if current == target {
			workspace.Status.Location.Target = ""
			break
		}

		_, err := c.rootWorkspaceShardLister.Get(clusters.ToClusterAwareKey(tenancyv1alpha1.RootCluster, target))
		if errors.IsNotFound(err) {
			klog.Infof("Cannot move to nonexistent shard %q", tenancyv1alpha1.RootCluster, workspace.Name, target)
		} else if err != nil {
			return err
		}

		klog.Infof("Moving workspace %q to %q", workspace.Name, workspace.Status.Location.Target)
		workspace.Status.Location.Current = workspace.Status.Location.Target
		workspace.Status.Location.Target = ""
	}

	// check scheduled shard. This has no influence on the workspace baseURL or shard assignment. This might be a trigger for
	// a movement controller in the future (or a human intervention) to move workspaces off a shard.
	if workspace.Status.Location.Current != "" {
		if shard, err := c.rootWorkspaceShardLister.Get(clusters.ToClusterAwareKey(tenancyv1alpha1.RootCluster, workspace.Status.Location.Current)); errors.IsNotFound(err) {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, tenancyv1alpha1.WorkspaceShardValidReasonShardNotFound, conditionsv1alpha1.ConditionSeverityError, fmt.Sprintf("ClusterWorkspaceShard %q got deleted.", workspace.Status.Location.Current))
		} else if err != nil {
			return err
		} else if valid, reason, message := isValidShard(shard); !valid {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, reason, conditionsv1alpha1.ConditionSeverityError, message)
		} else {
			conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceShardValid)
		}
	}

	switch workspace.Status.Phase {
	case "":
		workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseScheduling
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		// TODO(sttts): in the future this step is done by a workspace shard itself. I.e. moving to initializing is a step
		//              of acceptance of the workspace on that shard.
		if workspace.Status.Location.Current != "" && workspace.Status.BaseURL != "" {
			// do final quorum read to avoid race when the workspace shard is being deleted
			_, err := c.kcpClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, workspace.Status.Location.Current, metav1.GetOptions{})
			if err != nil {
				// reschedule
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
				return nil // nolint:nilerr
			}

			workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseInitializing
		}
	case tenancyv1alpha1.ClusterWorkspacePhaseInitializing:
		if len(workspace.Status.Initializers) == 0 {
			workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
		}
	}

	return nil
}

func isValidShard(shard *tenancyv1alpha1.ClusterWorkspaceShard) (valid bool, reason, message string) {
	return true, "", ""
}
