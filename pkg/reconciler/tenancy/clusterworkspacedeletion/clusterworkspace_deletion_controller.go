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
	"github.com/kcp-dev/logicalcluster"

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
	kcpClient kcpclient.ClusterInterface,
	metadataClient metadata.Interface,
	workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	discoverResourcesFn func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error),
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "workspace-deletion")

	c := &Controller{
		queue:           queue,
		kcpClient:       kcpClient,
		workspaceLister: workspaceInformer.Lister(),
	}

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	c.deleter = deletion.NewWorkspacedResourcesDeleter(metadataClient, discoverResourcesFn)
	c.workspaceSynced = workspaceInformer.Informer().HasSynced

	return c
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClient       kcpclient.ClusterInterface
	workspaceLister tenancylister.ClusterWorkspaceLister
	workspaceSynced cache.InformerSynced
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

	if !cache.WaitForNamedCacheSync("workspace-deletion", ctx.Done(), c.workspaceSynced) {
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

	err := c.process(ctx, key)

	if err == nil {
		// no error, forget this entry and return
		c.queue.Forget(key)
		return true
	}

	var estimate *deletion.ResourcesRemainingError
	if errors.As(err, &estimate) {
		t := estimate.Estimate/2 + 1
		klog.V(2).Infof("Content remaining in workspace %s, waiting %d seconds", key, t)
		c.queue.AddAfter(key, time.Duration(t)*time.Second)
	} else {
		// rather than wait for a full resync, re-add the workspace to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of workspace %v failed: %w", key, err))
	}

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	startTime := time.Now()

	defer func() {
		klog.V(4).Infof("Finished syncing workspace %q (%v)", key, time.Since(startTime))
	}()

	workspace, err := c.workspaceLister.Get(key)
	if apierrors.IsNotFound(err) {
		klog.Infof("Workspace has been deleted %v", key)
		return nil
	}
	if err != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve workspace %v from store: %w", key, err))
		return err
	}

	workspaceCopy := workspace.DeepCopy()
	if workspaceCopy.DeletionTimestamp.IsZero() {
		hasFinalizer := false
		for i := range workspaceCopy.Finalizers {
			if workspaceCopy.Finalizers[i] == deletion.WorkspaceFinalizer {
				hasFinalizer = true
				break
			}
		}

		if hasFinalizer {
			return nil
		}

		finalizerData := &metav1.PartialObjectMetadata{
			ObjectMeta: metav1.ObjectMeta{
				Finalizers: append(workspaceCopy.Finalizers, deletion.WorkspaceFinalizer),
			},
		}

		finalizerBytes, err := json.Marshal(finalizerData)
		if err != nil {
			return err
		}

		_, err = c.kcpClient.Cluster(logicalcluster.From(workspace)).TenancyV1alpha1().ClusterWorkspaces().Patch(
			ctx, workspace.Name, types.MergePatchType, finalizerBytes, metav1.PatchOptions{})
		return err
	}

	err = c.deleter.Delete(ctx, workspaceCopy)
	if err == nil {
		return c.finalizeWorkspace(ctx, workspaceCopy)
	}

	if patchErr := c.patchCondition(ctx, workspace, workspaceCopy); patchErr != nil {
		return patchErr
	}

	return err
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

	_, err = c.kcpClient.Cluster(logicalcluster.From(new)).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}

// finalizeNamespace removes the specified finalizer and finalizes the workspace
func (c *Controller) finalizeWorkspace(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	copiedFinalizers := []string{}
	for i := range workspace.Finalizers {
		if workspace.Finalizers[i] == deletion.WorkspaceFinalizer {
			continue
		}
		copiedFinalizers = append(copiedFinalizers, workspace.Finalizers[i])
	}
	if len(workspace.Finalizers) == len(copiedFinalizers) {
		return nil
	}

	finalizerBytes, err := json.Marshal(copiedFinalizers)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf("{\"metadata\": {\"finalizers\": %s}}", string(finalizerBytes))

	_, err = c.kcpClient.Cluster(logicalcluster.From(workspace)).TenancyV1alpha1().ClusterWorkspaces().Patch(
		ctx, workspace.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}
