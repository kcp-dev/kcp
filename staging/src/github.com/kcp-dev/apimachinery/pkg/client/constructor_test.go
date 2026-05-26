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

package client

import (
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
)

type fakeClient struct{ id int64 }

func newFakeCache(t *testing.T, builds *atomic.Int64) Cache[*fakeClient] {
	t.Helper()
	return NewCache(&rest.Config{}, &http.Client{}, &Constructor[*fakeClient]{
		NewForConfigAndClient: func(_ *rest.Config, _ *http.Client) (*fakeClient, error) {
			return &fakeClient{id: builds.Add(1)}, nil
		},
	})
}

func TestClientCache_CachesAcrossCalls(t *testing.T) {
	var builds atomic.Int64
	cache := newFakeCache(t, &builds)
	path := logicalcluster.NewPath("ws-a")

	first, err := cache.Cluster(path)
	if err != nil {
		t.Fatalf("first Cluster: %v", err)
	}
	second, err := cache.Cluster(path)
	if err != nil {
		t.Fatalf("second Cluster: %v", err)
	}
	if first != second {
		t.Fatalf("expected cached client to be reused, got %p then %p", first, second)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("expected constructor called once, got %d", got)
	}
}

func TestClientCache_EvictDropsAndStopsCaching(t *testing.T) {
	var builds atomic.Int64
	cache := newFakeCache(t, &builds)
	path := logicalcluster.NewPath("ws-evict")

	first, _ := cache.Cluster(path)
	cache.Evict(path)

	// After evict, Cluster() must build a fresh client (the old one is gone)
	// and must NOT re-cache: subsequent calls must keep producing fresh
	// clients, because the cluster has been signalled as deleted.
	second, _ := cache.Cluster(path)
	if first == second {
		t.Fatalf("expected a fresh client after Evict, got the cached one")
	}
	third, _ := cache.Cluster(path)
	if second == third {
		t.Fatalf("expected Cluster() to keep building post-eviction (not cache), got identical clients")
	}
	if got := builds.Load(); got != 3 {
		t.Fatalf("expected 3 builds (initial + 2 post-evict), got %d", got)
	}
}

func TestEvictCluster_FansOutToAllRegisteredCaches(t *testing.T) {
	var buildsA, buildsB atomic.Int64
	cacheA := newFakeCache(t, &buildsA)
	cacheB := newFakeCache(t, &buildsB)
	path := logicalcluster.NewPath("ws-fanout")

	if _, err := cacheA.Cluster(path); err != nil {
		t.Fatalf("cacheA Cluster: %v", err)
	}
	if _, err := cacheB.Cluster(path); err != nil {
		t.Fatalf("cacheB Cluster: %v", err)
	}

	EvictCluster(path)

	// Both caches should now treat the path as evicted and refuse to
	// re-cache.
	a1, _ := cacheA.Cluster(path)
	a2, _ := cacheA.Cluster(path)
	if a1 == a2 {
		t.Fatalf("cacheA: expected re-eviction marker, got cached client")
	}
	b1, _ := cacheB.Cluster(path)
	b2, _ := cacheB.Cluster(path)
	if b1 == b2 {
		t.Fatalf("cacheB: expected re-eviction marker, got cached client")
	}
}

// TestRegistry_PrunesGCdCaches verifies that caches dropped by their owners
// don't pin themselves alive through the registry. We can't directly force a
// weak pointer to nil, so we drop the strong references, force GC, trigger
// EvictCluster to compact, and assert the registry no longer holds the
// dropped caches.
func TestRegistry_PrunesGCdCaches(t *testing.T) {
	// Drop any caches retained by previous tests in this binary so we start
	// from a known floor.
	flushRegistry()
	if got := registrySize(); got != 0 {
		t.Fatalf("expected empty registry after flush, got %d entries", got)
	}

	var builds atomic.Int64
	// Scope the caches to an inner function so all strong references go
	// out of scope before we force GC.
	func() {
		for range 5 {
			c := newFakeCache(t, &builds)
			_, _ = c.Cluster(logicalcluster.NewPath("ws-gc"))
			_ = c
		}
	}()

	if got := registrySize(); got != 5 {
		t.Fatalf("expected 5 registry entries pre-GC, got %d", got)
	}

	// Drop our reference to anything that might be holding the caches and
	// give the GC a chance to reclaim. weak.Pointer reclamation needs the
	// underlying object to be unreachable AND the GC to have actually swept.
	const attempts = 50
	for range attempts {
		runtime.GC()
		runtime.GC()
		EvictCluster(logicalcluster.NewPath("trigger-prune"))
		if registrySize() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("registry did not prune dead entries: still has %d", registrySize())
}

func registrySize() int {
	evictorsMu.Lock()
	defer evictorsMu.Unlock()
	return len(evictors)
}

// flushRegistry forces the registry to compact, dropping any entries whose
// underlying cache has been collected. Test-only helper.
func flushRegistry() {
	for range 50 {
		runtime.GC()
		runtime.GC()
		before := registrySize()
		EvictCluster(logicalcluster.NewPath("flush"))
		if registrySize() == before {
			return
		}
	}
}
