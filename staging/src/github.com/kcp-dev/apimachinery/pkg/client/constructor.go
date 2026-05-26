/*
Copyright 2022 The kcp Authors.

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

package client

import (
	"net/http"
	"sync"
	"weak"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Constructor is a wrapper around a constructor method for the client of type R.
type Constructor[R any] struct {
	NewForConfigAndClient func(*rest.Config, *http.Client) (R, error)
}

// Cache is a client factory that caches previous results.
type Cache[R any] interface {
	ClusterOrDie(clusterPath logicalcluster.Path) R
	Cluster(clusterPath logicalcluster.Path) (R, error)
	// Evict drops the cached client for clusterPath, if any. Used to release
	// per-cluster client state (REST clients, codec factories, parsed
	// schemas) when a logical cluster is deleted. Safe to call concurrently
	// with Cluster / ClusterOrDie. No-op if the path is not cached.
	Evict(clusterPath logicalcluster.Path)
}

// evictorRef is the value the registry tracks via a weak pointer. Each
// clientCache holds its own ref as a struct field, so the ref stays alive
// exactly as long as the cache does. Once the cache becomes unreachable
// from outside the registry, ref dies with it and the weak entry can be
// pruned.
type evictorRef struct {
	evict func(clusterPath logicalcluster.Path)
}

var (
	evictorsMu sync.Mutex
	evictors   []weak.Pointer[evictorRef]
)

// registerEvictor adds ref to the weak registry. The registry must not hold
// a strong reference to ref — that would re-introduce the leak this whole
// mechanism exists to avoid.
func registerEvictor(ref *evictorRef) {
	evictorsMu.Lock()
	defer evictorsMu.Unlock()
	evictors = append(evictors, weak.Make(ref))
}

// EvictCluster notifies every registered cache that clusterPath has been
// deleted and its cached client (and everything that client transitively
// pins — REST client, codec factory, JSON decoder state, OpenAPI schemas)
// can be released. Wire this to a LogicalCluster delete handler to bound
// retained memory per workspace lifetime. See
// https://github.com/kcp-dev/kcp/issues/4071.
//
// Dead entries (caches whose only remaining reference was the weak entry
// in this registry) are pruned in-place during the iteration.
func EvictCluster(clusterPath logicalcluster.Path) {
	evictorsMu.Lock()
	live := evictors[:0]
	alive := make([]*evictorRef, 0, len(evictors))
	for _, wp := range evictors {
		ref := wp.Value()
		if ref == nil {
			continue
		}
		live = append(live, wp)
		alive = append(alive, ref)
	}
	evictors = live
	evictorsMu.Unlock()
	for _, ref := range alive {
		ref.evict(clusterPath)
	}
}

// NewCache creates a new client factory cache using the given constructor.
// The cache is auto-registered with the package-level EvictCluster fan-out
// so per-cluster entries can be released when a LogicalCluster is deleted.
// The registry holds the cache weakly: if all references to the returned
// Cache are dropped, it becomes eligible for GC and is pruned from the
// registry lazily.
func NewCache[R any](cfg *rest.Config, client *http.Client, constructor *Constructor[R]) Cache[R] {
	c := &clientCache[R]{
		cfg:         cfg,
		client:      client,
		constructor: constructor,

		RWMutex:              &sync.RWMutex{},
		clientsByClusterPath: map[logicalcluster.Path]R{},
		evicted:              map[logicalcluster.Path]struct{}{},
	}
	c.evictRef = &evictorRef{evict: c.Evict}
	registerEvictor(c.evictRef)
	return c
}

type clientCache[R any] struct {
	cfg         *rest.Config
	client      *http.Client
	constructor *Constructor[R]

	*sync.RWMutex
	clientsByClusterPath map[logicalcluster.Path]R
	// evicted records cluster paths that have been signalled as gone. Once
	// a path appears here, Cluster() returns freshly-built clients to any
	// in-flight caller but never re-caches them — caching for a deleted
	// cluster would reintroduce the leak this whole mechanism exists to
	// fix.
	//
	// Entries are never deleted, so the map grows with the lifetime set of
	// evicted paths. Per entry: ~16B string header + ~16-32B path bytes +
	// ~26B map-bucket overhead ≈ ~60B. 100k churned workspaces ≈ ~6MB,
	// which is bounded and not worth GCing.
	evicted map[logicalcluster.Path]struct{}

	// evictRef anchors the entry registered in the package-level evictor
	// registry. The registry holds it weakly, so this field is what keeps
	// the entry alive: when the cache is GC'd, evictRef dies with it and
	// the weak entry can be pruned lazily.
	evictRef *evictorRef
}

// ClusterOrDie returns a new client scoped to the given logical cluster, or panics if there
// is any error.
func (c *clientCache[R]) ClusterOrDie(clusterPath logicalcluster.Path) R {
	client, err := c.Cluster(clusterPath)
	if err != nil {
		// we ensure that the config is valid in the constructor, and we assume that any changes
		// we make to it during scoping will not make it invalid, in order to hide the error from
		// downstream callers (as it should forever be nil); this is slightly risky
		panic(err)
	}
	return client
}

// Cluster returns a new client scoped to the given logical cluster.
func (c *clientCache[R]) Cluster(clusterPath logicalcluster.Path) (R, error) {
	c.RLock()
	cachedClient, exists := c.clientsByClusterPath[clusterPath]
	_, evicted := c.evicted[clusterPath]
	c.RUnlock()
	if exists {
		return cachedClient, nil
	}

	cfg := SetCluster(rest.CopyConfig(c.cfg), clusterPath)
	instance, err := c.constructor.NewForConfigAndClient(cfg, c.client)
	if err != nil {
		var result R
		return result, err
	}
	if evicted {
		// The cluster has been signalled as gone. Hand the freshly-built
		// client to the in-flight caller so its request can complete, but
		// don't resurrect cached state for a deleted cluster.
		return instance, nil
	}

	c.Lock()
	defer c.Unlock()
	cachedClient, exists = c.clientsByClusterPath[clusterPath]
	if exists {
		return cachedClient, nil
	}
	if _, evicted := c.evicted[clusterPath]; evicted {
		// An Evict raced with this build, or completed between our RUnlock
		// and Lock. Same handling as above — return without caching.
		return instance, nil
	}

	c.clientsByClusterPath[clusterPath] = instance

	return instance, nil
}

// Evict drops the cached client for clusterPath, if any, and records the
// path so future Cluster() calls do not re-cache for it.
func (c *clientCache[R]) Evict(clusterPath logicalcluster.Path) {
	c.Lock()
	defer c.Unlock()
	delete(c.clientsByClusterPath, clusterPath)
	c.evicted[clusterPath] = struct{}{}
}
