package graph

import "sort"

// ClustersFor returns the clusters that the given user, with the
// given group memberships, has access to, paired with each cluster's
// FrontProxy endpoint.
//
// The result is the union of clusters reachable directly by the user
// and clusters reachable through any of the supplied groups.
// Duplicates are removed and the result is sorted by ClusterName for
// stable output.
func (g *Graph) ClustersFor(user string, groups []string) []AccessEndpointSlice {
	g.mu.RLock()
	defer g.mu.RUnlock()

	seen := make(map[LogicalCluster]struct{})

	// User's direct access.
	for c := range g.access[User(user)] {
		seen[c] = struct{}{}
	}
	// Group access.
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
