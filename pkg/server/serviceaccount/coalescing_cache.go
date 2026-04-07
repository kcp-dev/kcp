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

package serviceaccount

import (
	"fmt"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"

	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	// successCacheTTL is the TTL to cache a successful lookup.
	successCacheTTL = 1 * time.Minute
	// failureCacheTTL is the TTL to cache a failed lookup.
	failureCacheTTL = 10 * time.Second
)

type cacheKey struct {
	clusterName logicalcluster.Name
	types.NamespacedName
}

func (k cacheKey) String() string {
	return fmt.Sprintf("%s/%s/%s", k.clusterName, k.Namespace, k.Name)
}

type cacheValue[V any] struct {
	value V
	err   error
}

// coalescingCache deduplicates possible concurrent requests for the
// same key using singleflight, backed by a ttl cache.
type coalescingCache[V any] struct {
	ttl   *ttlcache.Cache[cacheKey, cacheValue[V]]
	group *singleflight.Group
}

func newCoalescingCache[V any]() coalescingCache[V] {
	return coalescingCache[V]{
		ttl:   ttlcache.New[cacheKey, cacheValue[V]](),
		group: &singleflight.Group{},
	}
}

func (c *coalescingCache[V]) Start() {
	go c.ttl.Start()
}

func (c *coalescingCache[V]) Stop() {
	c.ttl.Stop()
}

func (c *coalescingCache[V]) get(clusterName logicalcluster.Name, namespace, name string, fetch func() (V, error)) (V, error) {
	key := cacheKey{clusterName, types.NamespacedName{Namespace: namespace, Name: name}}

	// Do not refresh the TTL on hit to prevent often-used SAs living
	// forever even if they were deleted.
	if item := c.ttl.Get(key, ttlcache.WithDisableTouchOnHit[cacheKey, cacheValue[V]]()); item != nil {
		if !item.IsExpired() {
			cv := item.Value()
			return cv.value, cv.err
		}
	}

	result, err, _ := c.group.Do(key.String(), func() (interface{}, error) {
		v, err := fetch()
		cv := cacheValue[V]{value: v, err: err}
		ttl := successCacheTTL
		if err != nil {
			ttl = failureCacheTTL
		}
		c.ttl.Set(key, cv, ttl)
		return v, err
	})
	if err != nil {
		var zero V
		return zero, err
	}
	return result.(V), nil
}
