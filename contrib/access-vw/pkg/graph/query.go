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

package graph

import "sort"

// ClustersFor returns the clusters that the given user, with the
// given group memberships, has access to, paired with each cluster's
// FrontProxy endpoint.
func (g *Graph) ClustersFor(user string, groups []string) []AccessEndpointSlice {
	g.mu.RLock()
	defer g.mu.RUnlock()

	seen := make(map[LogicalCluster]struct{})

	for c := range g.access[User(user)] {
		seen[c] = struct{}{}
	}

	for _, group := range groups {
		for c := range g.access[Group(group)] {
			seen[c] = struct{}{}
		}
	}

	out := make([]AccessEndpointSlice, 0, len(seen))
	for c := range seen {
		out = append(out, AccessEndpointSlice{
			ClusterName: string(c),
			Endpoint:    g.endpoints[c],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClusterName < out[j].ClusterName })
	return out
}
