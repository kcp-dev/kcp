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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/clock"

	"github.com/kcp-dev/logicalcluster/v3"
)

const evictedClusterTTL = time.Minute

type ClusterAwareRateLimiter interface {
	flowcontrol.RateLimiter
	Cluster(clusterPath logicalcluster.Path) flowcontrol.RateLimiter
}

type GroupAwareRateLimiter interface {
	flowcontrol.RateLimiter
	Group(groupVersion schema.GroupVersion) flowcontrol.RateLimiter
}

var _ ClusterAwareRateLimiter = &ClusterRateLimiter{}

type ClusterRateLimiter struct {
	flowcontrol.RateLimiter

	qps   float32
	burst int

	lock     sync.Mutex
	limiters map[logicalcluster.Path]flowcontrol.RateLimiter
	evicted  map[logicalcluster.Path]time.Time
	clock    clock.PassiveClock

	evictRef *evictorRef
}

func NewClusterRateLimiter(qps float32, burst int) *ClusterRateLimiter {
	l := &ClusterRateLimiter{
		RateLimiter: flowcontrol.NewTokenBucketRateLimiter(qps, burst),
		qps:         qps,
		burst:       burst,
		limiters:    map[logicalcluster.Path]flowcontrol.RateLimiter{},
		evicted:     map[logicalcluster.Path]time.Time{},
		clock:       clock.RealClock{},
	}
	l.evictRef = &evictorRef{evict: l.Evict}
	registerEvictor(l.evictRef)
	return l
}

func (l *ClusterRateLimiter) Cluster(clusterPath logicalcluster.Path) flowcontrol.RateLimiter {
	l.lock.Lock()
	defer l.lock.Unlock()
	limiter, ok := l.limiters[clusterPath]
	if !ok {
		limiter = flowcontrol.NewTokenBucketRateLimiter(l.qps, l.burst)
		if l.recentlyEvicted(clusterPath) {
			return limiter
		}
		delete(l.evicted, clusterPath)
		l.limiters[clusterPath] = limiter
	}
	return limiter
}

func (l *ClusterRateLimiter) Evict(clusterPath logicalcluster.Path) {
	l.lock.Lock()
	defer l.lock.Unlock()
	delete(l.limiters, clusterPath)
	for path := range l.evicted {
		if !l.recentlyEvicted(path) {
			delete(l.evicted, path)
		}
	}
	l.evicted[clusterPath] = l.clock.Now()
}

func (l *ClusterRateLimiter) recentlyEvicted(clusterPath logicalcluster.Path) bool {
	evictedAt, ok := l.evicted[clusterPath]
	return ok && l.clock.Since(evictedAt) < evictedClusterTTL
}
