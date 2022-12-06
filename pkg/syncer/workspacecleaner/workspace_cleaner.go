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

package workspacecleaner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncerindexers "github.com/kcp-dev/kcp/pkg/syncer/indexers"
)

const (
	controllerName = "kcp-workload-syncer-workspace-cleaner"
)

// CleanupTenantFunc is a function that deletes resources associated to a tenant.
type CleanupTenantFunc func(ctx context.Context, tenantID string) error

// WorkspaceCleaner manages workspaces that don't contain any resource anymore,
// to cleanup related resources (like DNS resources or NetworkPolicies) after
// a given delay.
type WorkspaceCleaner struct {
	delayedQueue workqueue.RateLimitingInterface

	lock                sync.Mutex
	toDeleteMap         map[string]time.Time
	workspaceCleanDelay time.Duration

	listDownstreamNamespacesForTenantID func(tenantID string) ([]*unstructured.Unstructured, error)
	cleanupFuncs                        []CleanupTenantFunc
}

// NewWorkspaceCleaner creates a [WorkspaceCleaner] which manages workspaces
// that don't contain any resource anymore to cleanup related resources
// (like DNS resources or NetworkPolicies) after a given delay.
func NewWorkspaceCleaner(
	syncerLogger logr.Logger,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	workspaceCleanDelay time.Duration,
	cleanupFuncs ...CleanupTenantFunc,
) (*WorkspaceCleaner, error) {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	logger := logging.WithReconciler(syncerLogger, controllerName)

	c := WorkspaceCleaner{
		delayedQueue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		toDeleteMap:  make(map[string]time.Time),
		listDownstreamNamespacesForTenantID: func(tenantID string) ([]*unstructured.Unstructured, error) {
			nsInformer, err := ddsifForDownstream.ForResource(namespaceGVR)
			if err != nil {
				return nil, err
			}
			return indexers.ByIndex[*unstructured.Unstructured](nsInformer.Informer().GetIndexer(), syncerindexers.ByTenantIDIndexName, tenantID)
		},

		cleanupFuncs:        cleanupFuncs,
		workspaceCleanDelay: workspaceCleanDelay,
	}

	logger.V(2).Info("Set up downstream namespace informer")

	return &c, nil
}

func (c *WorkspaceCleaner) isPlannedForCleaning(key string) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	_, ok := c.toDeleteMap[key]
	return ok
}

func (c *WorkspaceCleaner) CancelCleaning(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.toDeleteMap, key)
}

func (c *WorkspaceCleaner) PlanCleaning(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if _, ok := c.toDeleteMap[key]; !ok {
		c.toDeleteMap[key] = time.Now()
		c.delayedQueue.AddAfter(key, c.workspaceCleanDelay)
	}
}

// Start starts N worker processes processing work items.
func (c *WorkspaceCleaner) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.delayedQueue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startDelayedWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *WorkspaceCleaner) startDelayedWorker(ctx context.Context) {
	logger := klog.FromContext(ctx).WithValues("queue", "delayed")
	ctx = klog.NewContext(ctx, logger)
	for c.processNextDelayedWorkItem(ctx) {
	}
}

func (c *WorkspaceCleaner) processNextDelayedWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.delayedQueue.Get()
	if quit {
		return false
	}
	tenantID := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), tenantID)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.delayedQueue.Done(key)

	if err := c.processDelayed(ctx, tenantID); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", controllerName, key, err))
		c.delayedQueue.AddRateLimited(key)
		return true
	}
	c.delayedQueue.Forget(key)
	return true
}

func (c *WorkspaceCleaner) processDelayed(ctx context.Context, tenantID string) error {
	logger := klog.FromContext(ctx)
	if !c.isPlannedForCleaning(tenantID) {
		logger.V(2).Info("Workspace is not marked for deletion anymore, skipping")
		return nil
	}

	tenantNamespaces, err := c.listDownstreamNamespacesForTenantID(tenantID)
	if err != nil {
		return fmt.Errorf("failed to retrieve the list of downstream namespaces for upstream workspace: %w", err)
	}
	if len(tenantNamespaces) > 0 {
		logger.V(2).Info("upstream workspace still has related downstream namespaces, skip cleaning now but keep it as a candidate for future cleaning")
		return nil
	}

	for _, cleanupFunc := range c.cleanupFuncs {
		if err := cleanupFunc(ctx, tenantID); err != nil {
			return err
		}
	}
	c.CancelCleaning(tenantID)
	return nil
}
