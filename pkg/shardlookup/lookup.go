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

package shardlookup

import (
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
)

// Lookup is a simple wrapper to abstract to local-or-fetch logic into clean functions.
type Lookup[V any] struct {
	cache   *TTLCache[V]
	isLocal func(clusterName logicalcluster.Name, namespace, name string) bool
	local   func(clusterName logicalcluster.Name, namespace, name string) (V, error)
	fetch   func(clusterName logicalcluster.Name, namespace, name string) (V, error)
}

// NewLookup creates a new Lookup backed by the given TTLCache.
// The [TTLCache] is used to cache objects and synchronize requests.
//
// isLocal determines if the key should be served from the local callback.
// local serves keys that are on the local shard, typically from an informer lister.
// fetch retrieves the value from a remote shard, typically via an API client.
func NewLookup[V any](
	cache *TTLCache[V],
	isLocal func(clusterName logicalcluster.Name, namespace, name string) bool,
	local func(clusterName logicalcluster.Name, namespace, name string) (V, error),
	fetch func(clusterName logicalcluster.Name, namespace, name string) (V, error),
) *Lookup[V] {
	return &Lookup[V]{
		cache:   cache,
		isLocal: isLocal,
		local:   local,
		fetch:   fetch,
	}
}

// Get returns the value for key.
func (l *Lookup[V]) Get(clusterName logicalcluster.Name, namespace, name string) (V, error) {
	if l.isLocal(clusterName, namespace, name) {
		return l.local(clusterName, namespace, name)
	}
	key := kcpcache.ToClusterAwareKey(clusterName.String(), namespace, name)
	return l.cache.Get(key, func() (V, error) {
		return l.fetch(clusterName, namespace, name)
	})
}
