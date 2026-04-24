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
	"context"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"
)

const (
	// DefaultSuccessTTL is the default TTL to cache a successful lookup.
	DefaultSuccessTTL = 1 * time.Minute
	// DefaultFailureTTL is the default TTL to cache a failed lookup.
	DefaultFailureTTL = 10 * time.Second
)

type cacheValue[V any] struct {
	value V
	err   error
}

// TTLOptions contains options for the [TTLCache].
type TTLOptions struct {
	// SuccessTTL is the TTL to cache a successful lookup. Defaults to DefaultSuccessTTL.
	SuccessTTL time.Duration
	// FailureTTL is the TTL to cache a failed lookup. Defaults to DefaultFailureTTL.
	FailureTTL time.Duration
}

// TTLCache caches objects in a TTL cache and synchronizes concurrent hits on the same key.
// Keys are strings, typically constructed via [kcpcache.ToClusterAwareKey].
type TTLCache[V any] struct {
	opts  TTLOptions
	ttl   *ttlcache.Cache[string, cacheValue[V]]
	group *singleflight.Group
}

// NewTTLCache creates a new Cache.
func NewTTLCache[V any]() *TTLCache[V] {
	return NewTTLCacheWithOptions[V](TTLOptions{})
}

// NewTTLCacheWithOptions creates a new Cache with the given [TTLOptions].
func NewTTLCacheWithOptions[V any](opts TTLOptions) *TTLCache[V] {
	c := &TTLCache[V]{
		opts:  opts,
		ttl:   ttlcache.New[string, cacheValue[V]](),
		group: &singleflight.Group{},
	}
	if int64(c.opts.SuccessTTL) == 0 {
		c.opts.SuccessTTL = DefaultSuccessTTL
	}
	if int64(c.opts.FailureTTL) == 0 {
		c.opts.FailureTTL = DefaultFailureTTL
	}
	return c
}

// Start begins the background goroutine that evicts expired entries.
//
// Running Start is not required for the [TTLCache] to function
// correctly, however not starting it will cause the cache to accrue
// stale items in memory until the process exits.
// Saving this goroutine can be a tradeoff if the amount of possible
// keys is known to be finite and low.
func (c *TTLCache[V]) Start() {
	go c.ttl.Start()
}

// Stop stops the goroutine evicting expired entries.
func (c *TTLCache[V]) Stop() {
	c.ttl.Stop()
}

// StartWithContext calls .Start and calls .Stop when the context is canceled.
func (c *TTLCache[V]) StartWithContext(ctx context.Context) {
	c.Start()
	go func() {
		<-ctx.Done()
		c.Stop()
	}()
}

// OnEviction registers a callback that is called when an entry is
// evicted from the cache. The callback runs on a separate goroutine.
// The returned function can be called to unsubscribe.
func (c *TTLCache[V]) OnEviction(fn func(value V)) func() {
	return c.ttl.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[string, cacheValue[V]]) {
		fn(item.Value().value)
	})
}

// Get returns the value for key.
// If the key is cached within the TTL the cached value is returned but
// the TTL is not refreshed to prevent frequently used items from living
// forever.
// If the key is not cached or is expired the value is retrieved using
// the provided function and cached.
func (c *TTLCache[V]) Get(key string, fetch func() (V, error)) (V, error) {
	if item := c.ttl.Get(key, ttlcache.WithDisableTouchOnHit[string, cacheValue[V]]()); item != nil {
		if !item.IsExpired() {
			cv := item.Value()
			return cv.value, cv.err
		}
	}

	result, err, _ := c.group.Do(key, func() (any, error) {
		v, err := fetch()
		cv := cacheValue[V]{value: v, err: err}
		ttl := c.opts.SuccessTTL
		if err != nil {
			var zero V
			cv.value = zero
			ttl = c.opts.FailureTTL
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
