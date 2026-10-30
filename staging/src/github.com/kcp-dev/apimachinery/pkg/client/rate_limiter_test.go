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
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/kcp-dev/logicalcluster/v3"
)

func newLimiterCache(limiter flowcontrol.RateLimiter) Cache[flowcontrol.RateLimiter] {
	return NewCache(&rest.Config{RateLimiter: limiter}, &http.Client{}, &Constructor[flowcontrol.RateLimiter]{
		NewForConfigAndClient: func(cfg *rest.Config, _ *http.Client) (flowcontrol.RateLimiter, error) {
			return cfg.RateLimiter, nil
		},
	})
}

//nolint:paralleltest
func TestClusterRateLimiter_PerClusterSharedAcrossCaches(t *testing.T) {
	limiter := NewClusterRateLimiter(1, 1)
	appsCache := newLimiterCache(limiter)
	coreCache := newLimiterCache(limiter)
	a := logicalcluster.NewPath("ws-a")
	b := logicalcluster.NewPath("ws-b")

	appsA := appsCache.ClusterOrDie(a)
	if appsA == flowcontrol.RateLimiter(limiter) {
		t.Fatalf("expected a per-cluster limiter, got the shared one")
	}
	if coreA := coreCache.ClusterOrDie(a); coreA != appsA {
		t.Fatalf("expected the same limiter for all clients of a cluster")
	}
	if appsB := appsCache.ClusterOrDie(b); appsB == appsA {
		t.Fatalf("expected different limiters for different clusters")
	}

	if !appsA.TryAccept() {
		t.Fatalf("expected first request of ws-a to be accepted")
	}
	if appsA.TryAccept() {
		t.Fatalf("expected ws-a to be throttled")
	}
	if !appsCache.ClusterOrDie(b).TryAccept() {
		t.Fatalf("expected ws-b not to be throttled by ws-a")
	}
}

//nolint:paralleltest
func TestClusterRateLimiter_EvictCluster(t *testing.T) {
	limiter := NewClusterRateLimiter(1, 1)
	path := logicalcluster.NewPath("ws-evict-limiter")

	first := limiter.Cluster(path)
	EvictCluster(path)
	if second := limiter.Cluster(path); second == first {
		t.Fatalf("expected a fresh limiter after EvictCluster")
	}
}

//nolint:paralleltest
func TestClusterRateLimiter_UserLimiterIsKept(t *testing.T) {
	user := flowcontrol.NewFakeAlwaysRateLimiter()
	if got := newLimiterCache(user).ClusterOrDie(logicalcluster.NewPath("ws-user")); got != user {
		t.Fatalf("expected the caller supplied limiter to be kept")
	}
}
