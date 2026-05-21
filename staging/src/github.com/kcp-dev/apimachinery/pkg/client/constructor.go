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

// Evictor is the non-generic subset of Cache used by EvictCluster to fan
// eviction out to every registered cache. Every cache returned by NewCache
// is auto-registered. Callers that wrap or substitute caches may register
// their own implementation via RegisterEvictor.
type Evictor interface {
	Evict(clusterPath logicalcluster.Path)
}

var (
	evictorsMu sync.RWMutex
	evictors   []Evictor
)

// RegisterEvictor adds e to the set of caches notified by EvictCluster.
// NewCache calls this automatically.
func RegisterEvictor(e Evictor) {
	evictorsMu.Lock()
	defer evictorsMu.Unlock()
	evictors = append(evictors, e)
}

// EvictCluster notifies every registered cache that clusterPath has been
// deleted and its cached client (and everything that client transitively
// pins — REST client, codec factory, JSON decoder state, OpenAPI schemas)
// can be released. Wire this to a LogicalCluster delete handler to bound
// retained memory per workspace lifetime. See
// https://github.com/kcp-dev/kcp/issues/4071.
func EvictCluster(clusterPath logicalcluster.Path) {
	evictorsMu.RLock()
	snapshot := make([]Evictor, len(evictors))
	copy(snapshot, evictors)
	evictorsMu.RUnlock()
	for _, e := range snapshot {
		e.Evict(clusterPath)
	}
}

// NewCache creates a new client factory cache using the given constructor.
// The cache is auto-registered with the package-level EvictCluster fan-out
// so per-cluster entries can be released when a LogicalCluster is deleted.
func NewCache[R any](cfg *rest.Config, client *http.Client, constructor *Constructor[R]) Cache[R] {
	c := &clientCache[R]{
		cfg:         cfg,
		client:      client,
		constructor: constructor,

		RWMutex:              &sync.RWMutex{},
		clientsByClusterPath: map[logicalcluster.Path]R{},
	}
	RegisterEvictor(c)
	return c
}

type clientCache[R any] struct {
	cfg         *rest.Config
	client      *http.Client
	constructor *Constructor[R]

	*sync.RWMutex
	clientsByClusterPath map[logicalcluster.Path]R
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
	var cachedClient R
	var exists bool
	c.RLock()
	cachedClient, exists = c.clientsByClusterPath[clusterPath]
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

	c.Lock()
	defer c.Unlock()
	cachedClient, exists = c.clientsByClusterPath[clusterPath]
	if exists {
		return cachedClient, nil
	}

	c.clientsByClusterPath[clusterPath] = instance

	return instance, nil
}

// Evict drops the cached client for clusterPath, if any.
func (c *clientCache[R]) Evict(clusterPath logicalcluster.Path) {
	c.Lock()
	defer c.Unlock()
	delete(c.clientsByClusterPath, clusterPath)
}
