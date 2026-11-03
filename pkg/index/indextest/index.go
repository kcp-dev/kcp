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

// Package indextest provides a test double for index.Index.
package indextest

import (
	"context"
	"sync/atomic"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
)

var _ index.Index = &Index{}

// Index is a map-based index.Index: it maps logical clusters to shards and
// shards to base URLs. Lookups resolve logical cluster names only, not
// workspace paths. The maps must not be changed once the Index is in use.
type Index struct {
	// Clusters maps a logical cluster to the name of the shard hosting it.
	Clusters map[logicalcluster.Name]string
	// Shards maps a shard name to its base URL.
	Shards map[string]string
	// RecentlyMigratedClusters are the clusters RecentlyMigrated reports.
	RecentlyMigratedClusters map[logicalcluster.Name]bool

	// Registered and Released count the ClusterContext calls and the
	// calls of the cancel functions they returned.
	Registered atomic.Int32
	Released   atomic.Int32
}

// Lookup returns the shard hosting the cluster, with the cluster's URL on
// that shard.
func (f *Index) Lookup(path logicalcluster.Path) (index.Result, bool) {
	name, ok := path.Name()
	if !ok {
		return index.Result{}, false
	}
	shard, ok := f.Clusters[name]
	if !ok {
		return index.Result{}, false
	}
	return index.Result{
		Shard:   shard,
		Cluster: name,
		URL:     f.Shards[shard] + name.Path().RequestPath(),
	}, true
}

// LookupURL is Lookup: the fake knows no mounts.
func (f *Index) LookupURL(path logicalcluster.Path) (index.Result, bool) {
	return f.Lookup(path)
}

// BaseURL returns the base URL of the shard.
func (f *Index) BaseURL(shardName string) (string, bool) {
	url, ok := f.Shards[shardName]
	return url, ok
}

// RecentlyMigrated reports whether the cluster is in RecentlyMigratedClusters.
func (f *Index) RecentlyMigrated(cluster logicalcluster.Name) bool {
	return f.RecentlyMigratedClusters[cluster]
}

// ClusterContext returns a cancellable child of parent and counts the
// registration and its release.
func (f *Index) ClusterContext(parent context.Context, cluster logicalcluster.Name) (context.Context, context.CancelFunc) {
	f.Registered.Add(1)
	ctx, cancel := context.WithCancel(parent)
	return ctx, func() {
		f.Released.Add(1)
		cancel()
	}
}
