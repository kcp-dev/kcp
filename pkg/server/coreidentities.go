/*
Copyright 2026 The kcp Authors.

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

package server

import (
	"context"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	configrootidentities "github.com/kcp-dev/kcp/config/root-identities"
	"github.com/kcp-dev/kcp/pkg/identity/corekey"
	"github.com/kcp-dev/kcp/pkg/reconciler/admin/coreidentityset"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
)

// CoreIdentities is the shard's state around the identities of the core
// APIExports: the mounted key, the resolved identities, the readiness check
// that reports them, and the local CoreIdentitySet that records them.
type CoreIdentities struct {
	// Key is the mounted core identities key (--core-identities-key-file),
	// or nil.
	Key *corekey.Key
	// DevFile is the parsed --root-identities-file, or nil. Development
	// only, root shard only.
	DevFile *configrootidentities.Identities
	// Identities are the resolved identities and where they came from.
	Identities *bootstrap.Identities
	// Local reads the shard's own CoreIdentitySet.
	Local *bootstrap.LocalCoreIdentitySet
	// Health is the readiness check "core-identities".
	Health *coreidentityset.Health

	// NonIdentityKcpClusterClient talks to the local shard without identity
	// injection; enough for the shard-local system:shard logical cluster.
	NonIdentityKcpClusterClient kcpclientset.ClusterInterface

	mu       sync.RWMutex
	conflict string
}

// SetConflict records a configuration conflict, e.g. a
// --root-identities-file entry disagreeing with an existing identity. It
// is reported in the CoreIdentitySet and makes the shard not-ready.
func (c *CoreIdentities) SetConflict(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conflict = msg
}

// Conflict returns the recorded configuration conflict, or "".
func (c *CoreIdentities) Conflict() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conflict
}

// publishCoreIdentitySet writes the shard's CoreIdentitySet from what is
// known right now, reading the other shards' sets and the root workspace's
// APIExports live from the cache server (informers may not be running yet),
// and updates the readiness check. It is used during startup; afterwards
// the controller keeps the object up to date.
func (s *Server) publishCoreIdentitySet(ctx context.Context) {
	logger := klog.FromContext(ctx)
	ci := s.CoreIdentities
	if ci == nil || ci.Identities == nil {
		return
	}

	in := coreidentityset.Input{
		ShardName:   s.Options.Extra.ShardName,
		ExportNames: ci.Identities.ExportNames(),
		Identities:  ci.Identities.Snapshot(),
		Conflict:    ci.Conflict(),
	}
	if ci.Key != nil {
		in.ObservedKeyID = ci.Key.ID()
	}

	var sets []*adminv1alpha1.CoreIdentitySet
	if list, err := s.KcpCacheClusterClient.AdminV1alpha1().CoreIdentitySets().List(ctx, metav1.ListOptions{}); err != nil {
		logger.V(3).Info("cannot list the other shards' CoreIdentitySets from the cache server", "err", err)
	} else {
		for i := range list.Items {
			if list.Items[i].Status.Shard == in.ShardName || list.Items[i].Name == in.ShardName {
				continue
			}
			sets = append(sets, &list.Items[i])
			in.Peers = append(in.Peers, list.Items[i])
		}
	}
	in.RootView = coreidentityset.RootView(sets, func() ([]*apisv1alpha2.APIExport, error) {
		list, err := s.KcpCacheClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		exports := make([]*apisv1alpha2.APIExport, 0, len(list.Items))
		for i := range list.Items {
			exports = append(exports, &list.Items[i])
		}
		return exports, nil
	})

	set, err := coreidentityset.Publish(ctx, ci.NonIdentityKcpClusterClient, in)
	if err != nil {
		logger.Error(err, "failed to publish CoreIdentitySet")
		return
	}
	ci.Health.Update(set)
}
