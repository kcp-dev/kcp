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

package server

import (
	"context"
	"sync"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
)

type evictable interface {
	Evict(logicalcluster.Path)
}

// clientCacheEvictor forwards LogicalCluster deletions to its registered evictables.
type clientCacheEvictor struct {
	lock       sync.Mutex
	evictables []evictable
}

func newClientCacheEvictor() *clientCacheEvictor {
	return &clientCacheEvictor{}
}

// Register adds c to the set notified on LogicalCluster deletion. Safe to
// call before or after Run.
func (e *clientCacheEvictor) Register(c evictable) {
	if c == nil {
		return
	}
	e.lock.Lock()
	defer e.lock.Unlock()
	e.evictables = append(e.evictables, c)
}

// Run starts the clientCacheEvictor process.
func (e *clientCacheEvictor) Run(ctx context.Context, informer corev1alpha1informers.LogicalClusterClusterInformer) {
	logger := klog.FromContext(ctx)
	_, _ = informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj any) {
			lc, ok := obj.(*corev1alpha1.LogicalCluster)
			if !ok {
				tombstone, tok := obj.(cache.DeletedFinalStateUnknown)
				if !tok {
					return
				}
				lc, ok = tombstone.Obj.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}
			}
			name := logicalcluster.From(lc)
			if name == "" {
				return
			}
			logger.V(4).Info("evicting per-cluster client caches", "logicalcluster", name)
			path := name.Path()
			e.lock.Lock()
			snapshot := make([]evictable, len(e.evictables))
			copy(snapshot, e.evictables)
			e.lock.Unlock()
			for _, c := range snapshot {
				c.Evict(path)
			}
		},
	})
}
