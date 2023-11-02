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

package synctarget

import (
	"context"
	"sync"

	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
)

type tunnelerReconciler struct {
	startedTunnelersLock sync.RWMutex
	startedTunnelers     map[proxyv1alpha1.TunnelWorkspace]tunnelerStopper

	startShardTunneler func(ctx context.Context, shardURL proxyv1alpha1.TunnelWorkspace)
}

func (c *tunnelerReconciler) reconcile(ctx context.Context, workspaceProxy *proxyv1alpha1.WorkspaceProxy) (reconcileStatus, error) {
	requiredShards := map[proxyv1alpha1.TunnelWorkspace]bool{}
	if workspaceProxy != nil {
		for _, shardURL := range workspaceProxy.Status.TunnelWorkspaces {
			requiredShards[shardURL] = true
		}
	}

	c.startedTunnelersLock.Lock()
	defer c.startedTunnelersLock.Unlock()

	// Remove obsolete tunnelers that don't have a shard anymore
	for shardURL, stopTunneler := range c.startedTunnelers {
		if _, ok := requiredShards[shardURL]; ok {
			// The tunnelers are still expected => don't remove them
			continue
		}
		// The tunnelers should not be running
		// Stop them and remove it from the list of started shard tunneler
		stopTunneler()
		delete(c.startedTunnelers, shardURL)
	}

	var errs []error
	// Create and start missing tunnelers
	for shardURL := range requiredShards {
		if _, ok := c.startedTunnelers[shardURL]; ok {
			// The tunnelers are already started
			continue
		}

		// Start the tunnelers
		shardTunnelerContext, cancelFunc := context.WithCancel(ctx)

		// Create the tunneler
		c.startShardTunneler(shardTunnelerContext, shardURL)
		c.startedTunnelers[shardURL] = tunnelerStopper(cancelFunc)
	}
	return reconcileStatusContinue, utilserrors.NewAggregate(errs)
}

type tunnelerStopper func()
