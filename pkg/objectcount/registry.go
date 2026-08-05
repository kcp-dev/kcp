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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// Registry tracks the total number of objects per logical cluster on this
// shard. A periodic etcd scan provides the authoritative base counts, and the
// admission plugin applies deltas (+1 per admitted create, -1 per delete)
// between scans. The effective count is base + delta. Every completed scan
// replaces the base and resets all deltas, so any drift (e.g. from writes that
// failed after admission) self-corrects within one scan interval.
type Registry struct {
	defaultLimit int64
	active       atomic.Bool

	mu    sync.RWMutex
	base  map[logicalcluster.Name]int64
	delta map[logicalcluster.Name]*atomic.Int64
}

// NewRegistry creates a Registry with the given shard-wide default limit.
// A defaultLimit <= 0 means no default limit is enforced.
func NewRegistry(defaultLimit int64) *Registry {
	return &Registry{
		defaultLimit: defaultLimit,
		base:         map[logicalcluster.Name]int64{},
		delta:        map[logicalcluster.Name]*atomic.Int64{},
	}
}

// DefaultLimit returns the shard-wide default limit.
func (r *Registry) DefaultLimit() int64 {
	return r.defaultLimit
}

// EnforcementActive reports whether any limit is potentially in effect on
// this shard. It is maintained by the scanner and serves as a cheap fast path
// for admission when the feature is unused.
func (r *Registry) EnforcementActive() bool {
	return r.active.Load()
}

// SetEnforcementActive is called by the scanner to enable or disable
// enforcement.
func (r *Registry) SetEnforcementActive(active bool) {
	r.active.Store(active)
}

// Count returns the effective object count for the given logical cluster.
func (r *Registry) Count(cluster logicalcluster.Name) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := r.base[cluster]
	if d, ok := r.delta[cluster]; ok {
		count += d.Load()
	}
	return count
}

// Inc records an admitted object creation in the given logical cluster.
func (r *Registry) Inc(cluster logicalcluster.Name) {
	r.deltaFor(cluster).Add(1)
}

// Dec records an object deletion in the given logical cluster.
func (r *Registry) Dec(cluster logicalcluster.Name) {
	r.deltaFor(cluster).Add(-1)
}

func (r *Registry) deltaFor(cluster logicalcluster.Name) *atomic.Int64 {
	r.mu.RLock()
	d, ok := r.delta[cluster]
	r.mu.RUnlock()
	if ok {
		return d
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.delta[cluster]; ok {
		return d
	}
	d = &atomic.Int64{}
	r.delta[cluster] = d
	return d
}

// ReplaceBase replaces the authoritative base counts with the result of a
// completed scan and resets all deltas.
func (r *Registry) ReplaceBase(counts map[logicalcluster.Name]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base = counts
	r.delta = map[logicalcluster.Name]*atomic.Int64{}
}

// LimitFor resolves the effective limit for a logical cluster from its
// annotations, falling back to the shard-wide default. A return value <= 0
// means no limit is enforced. An unparseable annotation value falls back to
// the default.
//
// The shard-wide default does not apply to the root and system logical
// clusters: the shard writes bootstrap content there after they are ready,
// and a default limit would break shard bootstrapping (and upgrades adding
// new root content). They can still be limited with an explicit annotation.
func (r *Registry) LimitFor(cluster logicalcluster.Name, annotations map[string]string) int64 {
	if v, ok := annotations[corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey]; ok {
		if limit, err := strconv.ParseInt(v, 10, 64); err == nil {
			return limit
		}
	}
	if cluster == core.RootCluster || strings.HasPrefix(string(cluster), "system:") {
		return 0
	}
	return r.defaultLimit
}
