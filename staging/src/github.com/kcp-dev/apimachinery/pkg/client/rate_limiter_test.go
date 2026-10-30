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
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/kcp-dev/logicalcluster/v3"
)

func newLimiterCache(limiter flowcontrol.RateLimiter) Cache[flowcontrol.RateLimiter] {
	return NewCache(&rest.Config{RateLimiter: limiter}, &http.Client{}, &Constructor[flowcontrol.RateLimiter]{
		NewForConfigAndClient: func(cfg *rest.Config, _ *http.Client) (flowcontrol.RateLimiter, error) {
			return cfg.RateLimiter, nil
		},
	})
}

type fakeRESTClient struct {
	rest.Interface
	groupVersion schema.GroupVersion
}

func (c fakeRESTClient) APIVersion() schema.GroupVersion {
	return c.groupVersion
}

type limiterClient struct {
	limiter      flowcontrol.RateLimiter
	groupVersion schema.GroupVersion
}

func (c *limiterClient) RESTClient() rest.Interface {
	return fakeRESTClient{groupVersion: c.groupVersion}
}

func newGroupLimiterCache(limiter flowcontrol.RateLimiter, groupVersion schema.GroupVersion) Cache[*limiterClient] {
	return NewCache(&rest.Config{RateLimiter: limiter}, &http.Client{}, &Constructor[*limiterClient]{
		NewForConfigAndClient: func(cfg *rest.Config, _ *http.Client) (*limiterClient, error) {
			return &limiterClient{limiter: cfg.RateLimiter, groupVersion: groupVersion}, nil
		},
	})
}

type layeredRateLimiter struct {
	flowcontrol.RateLimiter
	clusterPath logicalcluster.Path
}

func (l *layeredRateLimiter) Cluster(clusterPath logicalcluster.Path) flowcontrol.RateLimiter {
	return &layeredRateLimiter{RateLimiter: l.RateLimiter, clusterPath: clusterPath}
}

func (l *layeredRateLimiter) Group(groupVersion schema.GroupVersion) flowcontrol.RateLimiter {
	return &groupRateLimiter{RateLimiter: l.RateLimiter, clusterPath: l.clusterPath, groupVersion: groupVersion}
}

type groupRateLimiter struct {
	flowcontrol.RateLimiter
	clusterPath  logicalcluster.Path
	groupVersion schema.GroupVersion
}

func TestClusterRateLimiter_PerClusterSharedAcrossCaches(t *testing.T) {
	t.Parallel()

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

func TestClusterRateLimiter_EvictCluster(t *testing.T) {
	t.Parallel()

	clock := clocktesting.NewFakePassiveClock(time.Now())
	limiter := NewClusterRateLimiter(1, 1)
	limiter.clock = clock
	path := logicalcluster.NewPath("ws-evict-limiter")

	first := limiter.Cluster(path)
	EvictCluster(path)
	second := limiter.Cluster(path)
	if second == first {
		t.Fatalf("expected a fresh limiter after EvictCluster")
	}
	if third := limiter.Cluster(path); third == second {
		t.Fatalf("expected limiters of an evicted cluster not to be cached")
	}

	limiter.lock.Lock()
	_, ok := limiter.limiters[path]
	limiter.lock.Unlock()
	if ok {
		t.Fatalf("expected no limiter to be cached for an evicted cluster")
	}

	clock.SetTime(clock.Now().Add(evictedClusterTTL))
	fourth := limiter.Cluster(path)
	if fifth := limiter.Cluster(path); fifth != fourth {
		t.Fatalf("expected the limiter to be cached again after the eviction TTL")
	}
}

func TestClusterRateLimiter_UserLimiterIsKept(t *testing.T) {
	t.Parallel()

	user := flowcontrol.NewFakeAlwaysRateLimiter()
	if got := newLimiterCache(user).ClusterOrDie(logicalcluster.NewPath("ws-user")); got != user {
		t.Fatalf("expected the caller supplied limiter to be kept")
	}
}

func TestClusterRateLimiter_GroupAwareLimiter(t *testing.T) {
	t.Parallel()

	limiter := &layeredRateLimiter{RateLimiter: flowcontrol.NewFakeAlwaysRateLimiter()}
	rbac := schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"}
	path := logicalcluster.NewPath("ws-layered")

	got, ok := newGroupLimiterCache(limiter, rbac).ClusterOrDie(path).limiter.(*groupRateLimiter)
	if !ok {
		t.Fatalf("expected a group limiter, got %T", got)
	}
	if got.clusterPath != path || got.groupVersion != rbac {
		t.Fatalf("expected a limiter for %s %s, got %s %s", path, rbac, got.clusterPath, got.groupVersion)
	}
}
