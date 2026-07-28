package graph_test

import (
	"reflect"
	"testing"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

func endpoint(name string) string {
	return "https://kcp.example.com/clusters/" + name
}

func slice(name string) graph.AccessEndpointSlice {
	return graph.AccessEndpointSlice{ClusterName: name, Endpoint: endpoint(name)}
}

func TestGrant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*graph.Graph)
		user   string
		groups []string
		want   []graph.AccessEndpointSlice
	}{
		{
			name: "individual user",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{slice("ws-1")},
		},
		{
			name: "group reaches every member",
			setup: func(g *graph.Graph) {
				g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
				g.Grant(graph.Group("eng"), "ws-2", endpoint("ws-2"))
			},
			user:   "alice",
			groups: []string{"eng"},
			want:   []graph.AccessEndpointSlice{slice("ws-1"), slice("ws-2")},
		},
		{
			name: "idempotent",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
				g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{slice("ws-1")},
		},
		{
			name: "endpoint update overwrites",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-1", "https://old.example.com/clusters/ws-1")
				g.Grant(graph.User("alice"), "ws-1", "https://new.example.com/clusters/ws-1")
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{
				{ClusterName: "ws-1", Endpoint: "https://new.example.com/clusters/ws-1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tt.setup(g)
			got := g.ClustersFor(tt.user, tt.groups)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClustersFor(%q, %v) = %v, want %v", tt.user, tt.groups, got, tt.want)
			}
		})
	}
}

func TestClustersFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*graph.Graph)
		user   string
		groups []string
		want   []graph.AccessEndpointSlice
	}{
		{
			name: "union of user and groups",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-direct", endpoint("ws-direct"))
				g.Grant(graph.Group("eng"), "ws-eng", endpoint("ws-eng"))
				g.Grant(graph.Group("platform"), "ws-platform", endpoint("ws-platform"))
			},
			user:   "alice",
			groups: []string{"eng", "platform"},
			want:   []graph.AccessEndpointSlice{slice("ws-direct"), slice("ws-eng"), slice("ws-platform")},
		},
		{
			name: "disjoint subjects see only their own",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-alice", endpoint("ws-alice"))
				g.Grant(graph.User("bob"), "ws-bob", endpoint("ws-bob"))
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{slice("ws-alice")},
		},
		{
			name: "unknown user returns empty",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
			},
			user: "nobody",
			want: nil,
		},
		{
			name: "stable ordering by cluster name",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-z", endpoint("ws-z"))
				g.Grant(graph.User("alice"), "ws-a", endpoint("ws-a"))
				g.Grant(graph.User("alice"), "ws-m", endpoint("ws-m"))
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{slice("ws-a"), slice("ws-m"), slice("ws-z")},
		},
		{
			name: "deduplicates same cluster via user and group",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-shared", endpoint("ws-shared"))
				g.Grant(graph.Group("eng"), "ws-shared", endpoint("ws-shared"))
			},
			user:   "alice",
			groups: []string{"eng"},
			want:   []graph.AccessEndpointSlice{slice("ws-shared")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tt.setup(g)
			got := g.ClustersFor(tt.user, tt.groups)
			if tt.want == nil {
				if len(got) != 0 {
					t.Errorf("ClustersFor(%q, %v) = %v, want empty", tt.user, tt.groups, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClustersFor(%q, %v) = %v, want %v", tt.user, tt.groups, got, tt.want)
			}
		})
	}
}

func TestRevoke(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*graph.Graph)
		user   string
		groups []string
		want   []graph.AccessEndpointSlice
	}{
		{
			name: "removes access to one cluster",
			setup: func(g *graph.Graph) {
				g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
				g.Grant(graph.User("alice"), "ws-2", endpoint("ws-2"))
				g.Revoke(graph.User("alice"), "ws-1")
			},
			user: "alice",
			want: []graph.AccessEndpointSlice{slice("ws-2")},
		},
		{
			name: "idempotent on never-granted",
			setup: func(g *graph.Graph) {
				g.Revoke(graph.User("alice"), "ws-1")
			},
			user: "alice",
			want: nil,
		},
		{
			name: "revokes group access",
			setup: func(g *graph.Graph) {
				g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
				g.Revoke(graph.Group("eng"), "ws-1")
			},
			user:   "alice",
			groups: []string{"eng"},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tt.setup(g)
			got := g.ClustersFor(tt.user, tt.groups)
			if tt.want == nil {
				if len(got) != 0 {
					t.Errorf("after revoke: got %v, want empty", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("after revoke: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestForget(t *testing.T) {
	t.Parallel()

	g := graph.New()
	g.Grant(graph.User("alice"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.Group("eng"), "ws-1", endpoint("ws-1"))
	g.Grant(graph.User("alice"), "ws-2", endpoint("ws-2"))

	g.Forget("ws-1")

	got := g.ClustersFor("alice", []string{"eng"})
	want := []graph.AccessEndpointSlice{slice("ws-2")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after Forget(ws-1): got %v, want %v", got, want)
	}
}

func TestReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setReady bool
		want     bool
	}{
		{"new graph is not ready", false, false},
		{"after SetReady is ready", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			if tt.setReady {
				g.SetReady()
			}
			if got := g.Ready(); got != tt.want {
				t.Errorf("Ready() = %v, want %v", got, tt.want)
			}
		})
	}
}
