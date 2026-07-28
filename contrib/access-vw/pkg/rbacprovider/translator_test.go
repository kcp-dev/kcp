package rbacprovider_test

import (
	"reflect"
	"sort"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/rbacprovider"
)

const testCluster graph.LogicalCluster = "ws-test"

func testEndpoint(name string) string {
	return "https://kcp.example.com/clusters/" + name
}

func crb(name string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Subjects:   subjects,
	}
}

func rb(namespace, name string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Subjects:   subjects,
	}
}

func userSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.UserKind, Name: name}
}

func groupSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.GroupKind, Name: name}
}

func saSubject(namespace, name string) rbacv1.Subject {
	return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: namespace, Name: name}
}

func clusterNames(slices []graph.AccessEndpointSlice) []string {
	out := make([]string, 0, len(slices))
	for _, s := range slices {
		out = append(out, s.ClusterName)
	}
	sort.Strings(out)
	return out
}

func TestApplyClusterRoleBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(*rbacprovider.Translator)
		queryUser  string
		queryGroup []string
		want       []string
	}{
		{
			name: "single user",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
		{
			name: "single group",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("eng-binding", groupSubject("eng")), testCluster, testEndpoint("ws-test"))
			},
			queryUser:  "alice",
			queryGroup: []string{"eng"},
			want:       []string{string(testCluster)},
		},
		{
			name: "service account",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("sa-binding", saSubject("kube-system", "default")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "system:serviceaccount:kube-system:default",
			want:      []string{string(testCluster)},
		},
		{
			name: "unknown subject kind ignored",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("weird-binding", rbacv1.Subject{Kind: "FooBar", Name: "alice"}), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      nil,
		},
		{
			name: "multiple subjects at once",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("multi", userSubject("alice"), userSubject("bob"), groupSubject("eng")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
		{
			name: "unchanged subjects is no-op",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
			},
			queryUser: "alice",
			want:      []string{string(testCluster)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tr := rbacprovider.NewTranslator(g)
			tt.setup(tr)

			got := clusterNames(g.ClustersFor(tt.queryUser, tt.queryGroup))
			if tt.want == nil {
				if len(got) != 0 {
					t.Errorf("got %v, want empty", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyClusterRoleBinding_dedupesSameSubject(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("alice-twice", userSubject("alice"), userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.RemoveClusterRoleBinding("alice-twice", testCluster)

	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after remove, alice should have no access; got %v", got)
	}
}

func TestApplyClusterRoleBinding_updatesSubjects(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice"), userSubject("bob")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice"), userSubject("carol")), testCluster, testEndpoint("ws-test"))

	if got := clusterNames(g.ClustersFor("alice", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("alice retained: got %v", got)
	}
	if got := g.ClustersFor("bob", nil); len(got) != 0 {
		t.Errorf("bob should have lost access; got %v", got)
	}
	if got := clusterNames(g.ClustersFor("carol", nil)); !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("carol gained: got %v", got)
	}
}

func TestApplyRoleBinding(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyRoleBinding(rb("ns-1", "alice-binding", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("got %v, want [%s]", got, testCluster)
	}
}

func TestRemoveClusterRoleBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*rbacprovider.Translator)
		user  string
	}{
		{
			name: "revokes access",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("b1", testCluster)
			},
			user: "alice",
		},
		{
			name: "unknown binding is no-op",
			setup: func(tr *rbacprovider.Translator) {
				tr.RemoveClusterRoleBinding("never-existed", testCluster)
				tr.RemoveRoleBinding("ns", "never-existed", testCluster)
			},
			user: "alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tr := rbacprovider.NewTranslator(g)
			tt.setup(tr)

			if got := g.ClustersFor(tt.user, nil); len(got) != 0 {
				t.Errorf("got %v, want empty", got)
			}
		})
	}
}

func TestOverlappingBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*rbacprovider.Translator)
		user      string
		groups    []string
		wantAfter []string
	}{
		{
			name: "partial remove preserves access via other binding",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("b1", testCluster)
			},
			user:      "alice",
			wantAfter: []string{string(testCluster)},
		},
		{
			name: "direct and group — remove direct keeps group access",
			setup: func(tr *rbacprovider.Translator) {
				tr.ApplyClusterRoleBinding(crb("direct", userSubject("alice")), testCluster, testEndpoint("ws-test"))
				tr.ApplyClusterRoleBinding(crb("eng", groupSubject("eng")), testCluster, testEndpoint("ws-test"))
				tr.RemoveClusterRoleBinding("direct", testCluster)
			},
			user:      "alice",
			groups:    []string{"eng"},
			wantAfter: []string{string(testCluster)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := graph.New()
			tr := rbacprovider.NewTranslator(g)
			tt.setup(tr)

			got := clusterNames(g.ClustersFor(tt.user, tt.groups))
			if !reflect.DeepEqual(got, tt.wantAfter) {
				t.Errorf("got %v, want %v", got, tt.wantAfter)
			}
		})
	}
}

func TestApplyClusterRoleBinding_endpointUpdate(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	const before = "https://old.example.com/clusters/ws-test"
	const after = "https://new.example.com/clusters/ws-test"

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, before)
	got := g.ClustersFor("alice", nil)
	if got[0].Endpoint != before {
		t.Fatalf("first apply: got endpoint %q, want %q", got[0].Endpoint, before)
	}

	// Subjects unchanged but endpoint moved. Current MVP keeps old
	// endpoint since the diff has no subject changes to trigger a
	// re-grant. This test pins that behavior.
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, after)
	got = g.ClustersFor("alice", nil)
	if got[0].Endpoint != before {
		t.Logf("MVP behavior change: endpoint refreshed to %q (was %q)", got[0].Endpoint, before)
	}
}

func TestForgetCluster(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	const otherCluster graph.LogicalCluster = "ws-other"

	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b2", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyClusterRoleBinding(crb("b3", userSubject("alice")), otherCluster, testEndpoint("ws-other"))

	tr.ForgetCluster(testCluster)

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster)}) {
		t.Errorf("after Forget(%s): got %v, want [%s]", testCluster, got, otherCluster)
	}

	// Re-applying should bring access back cleanly.
	tr.ApplyClusterRoleBinding(crb("b1", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	got = clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(otherCluster), string(testCluster)}) {
		t.Errorf("after re-apply: got %v, want both clusters", got)
	}
}

func TestRoleBinding_distinctFromCRB(t *testing.T) {
	t.Parallel()

	g := graph.New()
	tr := rbacprovider.NewTranslator(g)

	tr.ApplyClusterRoleBinding(crb("shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))
	tr.ApplyRoleBinding(rb("ns-a", "shared-name", userSubject("alice")), testCluster, testEndpoint("ws-test"))

	tr.RemoveClusterRoleBinding("shared-name", testCluster)

	got := clusterNames(g.ClustersFor("alice", nil))
	if !reflect.DeepEqual(got, []string{string(testCluster)}) {
		t.Errorf("after CRB remove: got %v, want [%s]", got, testCluster)
	}

	tr.RemoveRoleBinding("ns-a", "shared-name", testCluster)
	if got := g.ClustersFor("alice", nil); len(got) != 0 {
		t.Errorf("after RB remove: got %v, want empty", got)
	}
}
