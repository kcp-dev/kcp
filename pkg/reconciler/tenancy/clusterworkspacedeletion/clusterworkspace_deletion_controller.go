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

package clusterworkspacedeletion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion/deletion"
)

func NewController(
	kcpClusterClient kcpclient.Interface,
	metadataClusterClient metadata.Interface,
	workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	discoverResourcesFn func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error),
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "workspace-deletion")

	c := &Controller{
		queue:                 queue,
		kcpClusterClient:      kcpClusterClient,
		metadataClusterClient: metadataClusterClient,
		workspaceLister:       workspaceInformer.Lister(),
		deleter:               deletion.NewWorkspacedResourcesDeleter(metadataClusterClient, discoverResourcesFn),
	}

	workspaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch obj := obj.(type) {
			case *tenancyv1alpha1.ClusterWorkspace:
				return !obj.DeletionTimestamp.IsZero()
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		},
	})

	return c
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient      kcpclient.Interface
	metadataClusterClient metadata.Interface

	workspaceLister tenancylister.ClusterWorkspaceLister
	deleter         deletion.WorkspaceResourcesDeleterInterface
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

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("Starting ClusterWorkspace Deletion controller")
	defer klog.Info("Shutting down ClusterWorkspace Deletion controller")

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

	klog.V(4).Infof("Processing key %q", key)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	startTime := time.Now()
	err := c.process(ctx, key)

	if err == nil {
		// no error, forget this entry and return
		c.queue.Forget(key)
		return true
	}

	var estimate *deletion.ResourcesRemainingError
	if errors.As(err, &estimate) {
		t := estimate.Estimate/2 + 1
		klog.V(2).Infof("Content remaining in workspace %s after %v, waiting %d seconds to continue: %v", key, time.Since(startTime), t, err)

		c.queue.AddAfter(key, time.Duration(t)*time.Second)
	} else {
		// rather than wait for a full resync, re-add the workspace to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of workspace %v failed: %w", key, err))
	}

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	workspace, deleteErr := c.workspaceLister.Get(key)
	if apierrors.IsNotFound(deleteErr) {
		klog.V(2).Infof("Workspace has been deleted %v", key)
		return nil
	}
	if deleteErr != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve workspace %v from store: %w", key, deleteErr))
		return deleteErr
	}

	if workspace.DeletionTimestamp.IsZero() {
		return nil
	}

	workspaceCopy := workspace.DeepCopy()

	klog.V(2).Infof("Deleting workspace %s", key)
	startTime := time.Now()
	deleteErr = c.deleter.Delete(ctx, workspaceCopy)
	if deleteErr == nil {
		klog.V(2).Infof("Finished deleting workspace %q content after %v", key, time.Since(startTime))
		return c.finalizeWorkspace(ctx, workspaceCopy)
	}

	if err := c.patchCondition(ctx, workspace, workspaceCopy); err != nil {
		return err
	}

	return deleteErr
}

func (c *Controller) patchCondition(ctx context.Context, old, new *tenancyv1alpha1.ClusterWorkspace) error {
	if equality.Semantic.DeepEqual(old.Status.Conditions, new.Status.Conditions) {
		return nil
	}

	oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
		Status: tenancyv1alpha1.ClusterWorkspaceStatus{
			Conditions: old.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for workspace %s: %w", old.Name, err)
	}

	newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			UID:             old.UID,
			ResourceVersion: old.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: tenancyv1alpha1.ClusterWorkspaceStatus{
			Conditions: new.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for workspace %s: %w", new.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for workspace %s: %w", new.Name, err)
	}

	klog.V(2).Infof("Patching workspace %s|%s: %s", logicalcluster.From(new), new.Name, string(patchBytes))
	_, err = c.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Patch(logicalcluster.WithCluster(ctx, logicalcluster.From(new)), new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}

// finalizeNamespace removes the specified finalizer and finalizes the workspace
func (c *Controller) finalizeWorkspace(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	for i := range workspace.Finalizers {
		if workspace.Finalizers[i] == deletion.WorkspaceFinalizer {
			workspace.Finalizers = append(workspace.Finalizers[:i], workspace.Finalizers[i+1:]...)

			klog.V(2).Infof("Removing finalizer from workspace %s|%s", logicalcluster.From(workspace), workspace.Name)
			_, err := c.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Update(
				logicalcluster.WithCluster(ctx, logicalcluster.From(workspace)), workspace, metav1.UpdateOptions{})
			return err
		}
	}

	return nil
}
