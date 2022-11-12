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

package workspacedeletion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacedeletion/deletion"
)

const (
	ControllerName = "kcp-clusterworkspacedeletion"
)

var (
	background        = metav1.DeletePropagationBackground
	backgroudDeletion = metav1.DeleteOptions{PropagationPolicy: &background}
)

func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	metadataClusterClient kcpmetadata.ClusterInterface,
	workspaceInformer tenancyv1alpha1informers.ClusterWorkspaceClusterInformer,
	discoverResourcesFn func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error),
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:                 queue,
		kubeClusterClient:     kubeClusterClient,
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

	kubeClusterClient     kcpkubernetesclientset.ClusterInterface
	kcpClusterClient      kcpclientset.ClusterInterface
	metadataClusterClient kcpmetadata.ClusterInterface

	workspaceLister tenancyv1alpha1listers.ClusterWorkspaceClusterLister
	deleter         deletion.WorkspaceResourcesDeleterInterface
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing ClusterWorkspace")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

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
		duration := time.Duration(t) * time.Second
		logger.V(2).Error(err, "content remaining in workspace after a wait, waiting more to continue", "duration", time.Since(startTime), "waiting", duration)

		c.queue.AddAfter(key, duration)
	} else {
		// rather than wait for a full resync, re-add the workspace to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of workspace %v failed: %w", key, err))
	}

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	workspace, deleteErr := c.workspaceLister.Cluster(parent).Get(name)
	if apierrors.IsNotFound(deleteErr) {
		logger.V(2).Info("ClusterWorkspace has been deleted")
		return nil
	}
	if deleteErr != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve workspace %v from store: %w", key, deleteErr))
		return deleteErr
	}

	logger = logging.WithObject(logger, workspace)
	ctx = klog.NewContext(ctx, logger)

	if workspace.DeletionTimestamp.IsZero() {
		return nil
	}

	workspaceCopy := workspace.DeepCopy()

	logger.V(2).Info("deleting ClusterWorkspace")
	startTime := time.Now()
	deleteErr = c.deleter.Delete(ctx, workspaceCopy)
	if deleteErr == nil {
		logger.V(2).Info("finished deleting ClusterWorkspace content", "duration", time.Since(startTime))
		return c.finalizeWorkspace(ctx, workspaceCopy)
	}

	if err := c.patchCondition(ctx, workspace, workspaceCopy); err != nil {
		return err
	}

	return deleteErr
}

func (c *Controller) patchCondition(ctx context.Context, old, new *tenancyv1alpha1.ClusterWorkspace) error {
	logger := klog.FromContext(ctx)
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

	logger.V(2).Info("patching ClusterWorkspace", "patch", string(patchBytes))
	_, err = c.kcpClusterClient.Cluster(logicalcluster.From(new)).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}

// finalizeNamespace removes the specified finalizer and finalizes the workspace
func (c *Controller) finalizeWorkspace(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	logger := klog.FromContext(ctx)
	for i := range workspace.Finalizers {
		if workspace.Finalizers[i] == deletion.WorkspaceFinalizer {
			workspace.Finalizers = append(workspace.Finalizers[:i], workspace.Finalizers[i+1:]...)

			clusterName := logicalcluster.From(workspace)
			listOpts := metav1.ListOptions{
				LabelSelector: helper.WorkspaceLabelSelector(workspace.Name),
			}

			// TODO(hasheddan): ClusterRole and ClusterRoleBinding cleanup
			// should be handled by garbage collection when the controller is
			// implemented.
			if err := c.kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoles().DeleteCollection(ctx, backgroudDeletion, listOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("could not delete clusterroles for workspace %s: %w", clusterName, err)
			}
			if err := c.kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoleBindings().DeleteCollection(ctx, backgroudDeletion, listOpts); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("could not delete clusterrolebindings for workspace %s: %w", clusterName, err)
			}
			logger.V(2).Info("removing finalizer from ClusterWorkspace")
			_, err := c.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Update(ctx, workspace, metav1.UpdateOptions{})
			return err
		}
	}

	return nil
}
