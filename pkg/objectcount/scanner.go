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

package objectcount

import (
	"context"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/server/metrics"
)

// Scanner periodically counts all objects per logical cluster on this shard
// by scanning etcd keys and feeds the results into a Registry.
type Scanner struct {
	kv       clientv3.KV
	prefix   string
	interval time.Duration
	registry *Registry
	lcLister corev1alpha1listers.LogicalClusterClusterLister
	shard    string

	// published tracks the logical clusters currently exposed as metrics so
	// their label sets can be deleted when they drop below the threshold.
	published sets.Set[logicalcluster.Name]
}

// NewScanner creates a Scanner. prefix is the etcd storage prefix of this
// shard.
func NewScanner(
	kv clientv3.KV,
	prefix string,
	interval time.Duration,
	registry *Registry,
	lcLister corev1alpha1listers.LogicalClusterClusterLister,
	shardName string,
) *Scanner {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Scanner{
		kv:        kv,
		prefix:    prefix,
		interval:  interval,
		registry:  registry,
		lcLister:  lcLister,
		shard:     shardName,
		published: sets.New[logicalcluster.Name](),
	}
}

// Start runs the scanner until ctx is done.
func (s *Scanner) Start(ctx context.Context) {
	logger := klog.FromContext(ctx).WithValues("component", "objectcount-scanner")
	ctx = klog.NewContext(ctx, logger)
	wait.UntilWithContext(ctx, s.tick, s.interval)
}

func (s *Scanner) tick(ctx context.Context) {
	logger := klog.FromContext(ctx)

	lcs, err := s.lcLister.List(labels.Everything())
	if err != nil {
		logger.Error(err, "failed to list logical clusters, skipping object count scan")
		return
	}

	// Limits per logical cluster and the set of known cluster names, used both
	// for enforcement gating and for disambiguating etcd keys.
	limits := make(map[logicalcluster.Name]int64, len(lcs))
	clusterNames := sets.New[string]()
	anyLimited := false
	for _, lc := range lcs {
		name := logicalcluster.From(lc)
		clusterNames.Insert(string(name))
		limit := s.registry.LimitFor(name, lc.Annotations)
		limits[name] = limit
		if limit > 0 {
			anyLimited = true
		}
	}

	active := s.registry.DefaultLimit() > 0 || anyLimited
	s.registry.SetEnforcementActive(active)
	if !active {
		// Feature unused on this shard: skip the etcd scan and retract all
		// published metrics.
		for cluster := range s.published {
			metrics.DeleteLogicalClusterObjectCount(s.shard, string(cluster))
		}
		s.published = sets.New[logicalcluster.Name]()
		return
	}

	counts, err := s.scanOnce(ctx, clusterNames)
	if err != nil {
		logger.Error(err, "failed to scan etcd for object counts, keeping previous counts")
		return
	}

	s.registry.ReplaceBase(counts)
	s.publishMetrics(counts, limits)
}

// scanOnce performs one keys-only paginated pass over the whole storage
// prefix and buckets object counts per logical cluster.
func (s *Scanner) scanOnce(ctx context.Context, clusterNames sets.Set[string]) (map[logicalcluster.Name]int64, error) {
	isCluster := func(segment string) bool {
		return strings.HasPrefix(segment, "system:") || clusterNames.Has(segment)
	}

	counts := map[logicalcluster.Name]int64{}

	key := s.prefix
	for {
		resp, err := s.kv.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(s.prefix)),
			clientv3.WithKeysOnly(),
			clientv3.WithLimit(kcpetcd.ScanPageSize),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, kv := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}

			key := string(kv.Key)
			// Events are not counted by the admission plugin either: they are
			// short-lived, auto-generated, and needed to debug an exhausted
			// logical cluster.
			if rest := strings.TrimPrefix(key, s.prefix); strings.HasPrefix(rest, "core/events/") || strings.HasPrefix(rest, "events.k8s.io/events/") {
				continue
			}

			cluster, ok := kcpetcd.ClusterOf(s.prefix, key, isCluster)
			if !ok {
				continue
			}
			counts[cluster]++
		}

		if !resp.More {
			return counts, nil
		}
		key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

// publishMetrics publishes count/limit gauges for logical clusters at or
// above 90% of their limit and retracts gauges for all others.
func (s *Scanner) publishMetrics(counts map[logicalcluster.Name]int64, limits map[logicalcluster.Name]int64) {
	published := sets.New[logicalcluster.Name]()
	for cluster, limit := range limits {
		if limit <= 0 {
			continue
		}
		count := counts[cluster]
		if count*10 >= limit*9 {
			metrics.SetLogicalClusterObjectCount(s.shard, string(cluster), count, limit)
			published.Insert(cluster)
		}
	}

	for cluster := range s.published.Difference(published) {
		metrics.DeleteLogicalClusterObjectCount(s.shard, string(cluster))
	}
	s.published = published
}
