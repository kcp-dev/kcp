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

// Package graph provides an in-memory RBAC permission map for kcp.
//
// The graph is the shared seam between providers and the SCAR HTTP
// handler: providers (the kcp-native RBAC reconciler, an external/FGA
// integration, etc.) populate the graph with Grant/Revoke calls, and
// the handler reads from it via ClustersFor. Providers depend on this
// package; the SCAR handler depends on this package; nothing in this
// package depends on either of them.
//
// The package deliberately has no kcp imports so it stays reusable by
// other consumers (admin tooling, FrontProxy optimisations, future
// Warrants/Scopes evaluators if those land).
package graph

import (
	"sort"
	"sync"
	"sync/atomic"
)

type SubjectKind string

const (
	SubjectKindUser  SubjectKind = "User"
	SubjectKindGroup SubjectKind = "Group"
)

type Subject struct {
	Kind SubjectKind
	Name string
}

func User(name string) Subject {
	return Subject{Kind: SubjectKindUser, Name: name}
}

func Group(name string) Subject {
	return Subject{Kind: SubjectKindGroup, Name: name}
}

type LogicalCluster string

// AccessEndpointSlice is a single (cluster name, FrontProxy endpoint)
// pair returned to a caller.
type AccessEndpointSlice struct {
	// ClusterName is the LogicalCluster identifier.
	ClusterName string `json:"clusterName"`
	// Endpoint is the FrontProxy URL for this cluster.
	Endpoint string `json:"endpoint"`
}

// Graph is an in-memory RBAC permission map.
type Graph struct {
	mu        sync.RWMutex
	access    map[Subject]map[LogicalCluster]struct{}
	endpoints map[LogicalCluster]string
	ready     atomic.Bool
}

// New returns a new empty Graph.
func New() *Graph {
	return &Graph{
		access:    make(map[Subject]map[LogicalCluster]struct{}),
		endpoints: make(map[LogicalCluster]string),
	}
}

// Grant records that subject has access to cluster, reachable at
// the given endpoint URL.
func (g *Graph) Grant(subject Subject, cluster LogicalCluster, endpoint string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access[subject] == nil {
		g.access[subject] = make(map[LogicalCluster]struct{})
	}
	g.access[subject][cluster] = struct{}{}
	g.endpoints[cluster] = endpoint
}

// SetEndpoint updates the URL a cluster is reachable at without
// touching who can access it. Providers call this when a shard or
// front-proxy URL moves for a cluster whose bindings are unchanged;
// without it the graph would keep serving the old URL until the next
// subject change.
func (g *Graph) SetEndpoint(cluster LogicalCluster, endpoint string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, known := g.endpoints[cluster]; !known {
		return
	}
	g.endpoints[cluster] = endpoint
}

// Revoke removes subject's access to cluster. When no subject can
// reach the cluster any more, its endpoint is dropped too, so
// Snapshot does not accumulate entries for clusters nobody can see.
func (g *Graph) Revoke(subject Subject, cluster LogicalCluster) {
	g.mu.Lock()
	defer g.mu.Unlock()
	clusters, ok := g.access[subject]
	if !ok {
		return
	}
	delete(clusters, cluster)
	if len(clusters) == 0 {
		delete(g.access, subject)
	}
	if !g.anyAccessLocked(cluster) {
		delete(g.endpoints, cluster)
	}
}

func (g *Graph) anyAccessLocked(cluster LogicalCluster) bool {
	for _, clusters := range g.access {
		if _, ok := clusters[cluster]; ok {
			return true
		}
	}
	return false
}

// Forget removes a cluster entirely: every subject's access to it,
// and the cluster's recorded endpoint. Providers should call this
// when a cluster is deleted from the underlying source so stale
// endpoints don't accumulate.
func (g *Graph) Forget(cluster LogicalCluster) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for subject, clusters := range g.access {
		if _, ok := clusters[cluster]; ok {
			delete(clusters, cluster)
			if len(clusters) == 0 {
				delete(g.access, subject)
			}
		}
	}
	delete(g.endpoints, cluster)
}

// SetReady marks the graph as having completed its initial sync.
func (g *Graph) SetReady() {
	g.ready.Store(true)
}

// Ready reports whether the graph has completed its initial sync and
// is ready to serve accurate queries.
func (g *Graph) Ready() bool {
	return g.ready.Load()
}

// Snapshot is a point-in-time view of the graph for diagnostics.
type Snapshot struct {
	Ready    bool                `json:"ready"`
	Subjects map[string][]string `json:"subjects"` // subject → cluster names
	Clusters map[string]string   `json:"clusters"` // cluster name → endpoint
}

// Snapshot returns a read-consistent, JSON-friendly view of the graph.
func (g *Graph) Snapshot() Snapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()

	subjects := make(map[string][]string, len(g.access))
	for subj, clusters := range g.access {
		key := string(subj.Kind) + ":" + subj.Name
		names := make([]string, 0, len(clusters))
		for c := range clusters {
			names = append(names, string(c))
		}
		sort.Strings(names)
		subjects[key] = names
	}

	endpoints := make(map[string]string, len(g.endpoints))
	for c, ep := range g.endpoints {
		endpoints[string(c)] = ep
	}

	return Snapshot{
		Ready:    g.ready.Load(),
		Subjects: subjects,
		Clusters: endpoints,
	}
}
