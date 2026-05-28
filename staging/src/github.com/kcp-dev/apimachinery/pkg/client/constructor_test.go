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
	"sync/atomic"
	"testing"

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
