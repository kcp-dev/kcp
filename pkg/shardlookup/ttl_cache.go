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

// Delete removes key and runs the eviction callbacks for its value.
func (c *TTLCache[V]) Delete(key string) {
	c.ttl.Delete(key)
}

// Get returns the value for key.
// If the key is cached within the TTL the cached value is returned but
// the TTL is not refreshed to prevent frequently used items from living
// forever.
//
// If stale is non-nil the function is called with the cached value.
// If the stale function returns true the cached value is evicted and
// reloaded.
//
// If the key is not cached, is expired or was evicted as stale the value
// is retrieved using fetch and cached.
func (c *TTLCache[V]) Get(key string, fetch func() (V, error), stale func(V) bool) (V, error) {
	// a fast-path duplicate of the check running within the singleflight group below
	if item := c.ttl.Get(key, ttlcache.WithDisableTouchOnHit[string, cacheValue[V]]()); item != nil && !item.IsExpired() {
		// item is present and not expired
		cv := item.Value()
		if cv.err != nil {
			var zero V
			return zero, cv.err
		}
		// if stale was passed validate it
		if stale == nil || !stale(cv.value) {
			return cv.value, nil
		}
	}

	result, err, _ := c.group.Do(key, func() (any, error) {
		if item := c.ttl.Get(key, ttlcache.WithDisableTouchOnHit[string, cacheValue[V]]()); item != nil && !item.IsExpired() {
			// item is present and not expired
			cv := item.Value()
			if cv.err != nil {
				var zero V
				return zero, cv.err
			}
			// if stale was passed validate it
			if stale == nil || !stale(cv.value) {
				return cv.value, nil
			}
			// stale was passed and invalidated the value, evict it
			c.ttl.Delete(key)
		}

		// fetch a new value and return that
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
