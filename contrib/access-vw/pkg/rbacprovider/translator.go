// Package rbacprovider implements the kcp-native RBAC AccessProvider.
//
// The provider observes ClusterRoleBindings and RoleBindings across
// kcp shards and projects them onto the shared access graph: each
// binding contributes (Subject, LogicalCluster) edges, the graph
// sums them, and the SCAR HTTP handler reads from it.
package rbacprovider

import (
	"sync"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

type bindingKey struct {
	cluster   graph.LogicalCluster
	namespace string
	name      string
}

type bindingState struct {
	subjects []graph.Subject
	endpoint string
}

// Translator turns RBAC binding events into graph mutations.
type Translator struct {
	g *graph.Graph

	mu       sync.Mutex
	refs     map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}
	bindings map[bindingKey]bindingState
}

// NewTranslator returns a Translator that will emit Grant/Revoke
// calls on g.
func NewTranslator(g *graph.Graph) *Translator {
	return &Translator{
		g:        g,
		refs:     make(map[graph.Subject]map[graph.LogicalCluster]map[bindingKey]struct{}),
		bindings: make(map[bindingKey]bindingState),
	}
}

// ApplyClusterRoleBinding records the effect of a ClusterRoleBinding
// observed in the given logical cluster, addressable at endpoint.
func (t *Translator) ApplyClusterRoleBinding(crb *rbacv1.ClusterRoleBinding, cluster graph.LogicalCluster, endpoint string) {
	key := bindingKey{cluster: cluster, name: crb.Name}
	t.apply(key, translateSubjects(crb.Subjects, ""), endpoint)
}

// ApplyRoleBinding is the namespaced analogue of ApplyClusterRoleBinding.
func (t *Translator) ApplyRoleBinding(rb *rbacv1.RoleBinding, cluster graph.LogicalCluster, endpoint string) {
	key := bindingKey{cluster: cluster, namespace: rb.Namespace, name: rb.Name}
	t.apply(key, translateSubjects(rb.Subjects, rb.Namespace), endpoint)
}

// RemoveClusterRoleBinding undoes a previously-applied CRB:
// every (subject, cluster) edge it contributed loses one reference,
// and any edge whose ref count reaches zero is Revoked on the graph.
func (t *Translator) RemoveClusterRoleBinding(name string, cluster graph.LogicalCluster) {
	t.remove(bindingKey{cluster: cluster, name: name})
}

// RemoveRoleBinding is the namespaced analogue of RemoveClusterRoleBinding.
func (t *Translator) RemoveRoleBinding(namespace, name string, cluster graph.LogicalCluster) {
	t.remove(bindingKey{cluster: cluster, namespace: namespace, name: name})
}

// ForgetCluster removes every binding observed in the given cluster
// and clears the cluster's endpoint from the graph. Used when a
// workspace itself is deleted.
func (t *Translator) ForgetCluster(cluster graph.LogicalCluster) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key := range t.bindings {
		if key.cluster == cluster {
			t.removeLocked(key)
		}
	}
	t.g.Forget(cluster)
}

func (t *Translator) apply(key bindingKey, subjects []graph.Subject, endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	oldState, hasOld := t.bindings[key]
	t.bindings[key] = bindingState{subjects: subjects, endpoint: endpoint}

	oldSet := subjectSet(oldState.subjects)
	newSet := subjectSet(subjects)

	// Subjects in old but not new: lose a reference for this key.
	if hasOld {
		for s := range oldSet {
			if _, in := newSet[s]; !in {
				t.decrementRef(s, key.cluster, key)
			}
		}
	}

	// Subjects in new but not already counted under this key: gain one.
	for s := range newSet {
		if hasOld {
			if _, in := oldSet[s]; in {
				continue
			}
		}
		t.incrementRef(s, key.cluster, endpoint, key)
	}

	if hasOld && oldState.endpoint != endpoint {
		t.g.SetEndpoint(key.cluster, endpoint)
	}
}

func (t *Translator) remove(key bindingKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removeLocked(key)
}

func (t *Translator) removeLocked(key bindingKey) {
	state, ok := t.bindings[key]
	if !ok {
		return
	}
	delete(t.bindings, key)
	for _, s := range state.subjects {
		t.decrementRef(s, key.cluster, key)
	}
}

func (t *Translator) incrementRef(s graph.Subject, c graph.LogicalCluster, endpoint string, key bindingKey) {
	if t.refs[s] == nil {
		t.refs[s] = make(map[graph.LogicalCluster]map[bindingKey]struct{})
	}
	if t.refs[s][c] == nil {
		t.refs[s][c] = make(map[bindingKey]struct{})
	}
	first := len(t.refs[s][c]) == 0
	t.refs[s][c][key] = struct{}{}
	if first {
		t.g.Grant(s, c, endpoint)
	}
}

func (t *Translator) decrementRef(s graph.Subject, c graph.LogicalCluster, key bindingKey) {
	if t.refs[s] == nil || t.refs[s][c] == nil {
		return
	}
	delete(t.refs[s][c], key)
	if len(t.refs[s][c]) == 0 {
		delete(t.refs[s], c)
		if len(t.refs[s]) == 0 {
			delete(t.refs, s)
		}
		t.g.Revoke(s, c)
	}
}

// translateSubjects converts RBAC subjects to graph subjects, dropping
// kinds the graph does not model and de-duplicating the result.
// defaultNamespace is used for ServiceAccount subjects that omit one;
// pass "" when there is no meaningful default, and such subjects are
// skipped.
func translateSubjects(in []rbacv1.Subject, defaultNamespace string) []graph.Subject {
	seen := make(map[graph.Subject]struct{})
	out := make([]graph.Subject, 0, len(in))
	for _, rs := range in {
		s, ok := translateSubject(rs, defaultNamespace)
		if !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func translateSubject(rs rbacv1.Subject, defaultNamespace string) (graph.Subject, bool) {
	switch rs.Kind {
	case rbacv1.UserKind:
		return graph.User(rs.Name), true
	case rbacv1.GroupKind:
		return graph.Group(rs.Name), true
	case rbacv1.ServiceAccountKind:
		namespace := rs.Namespace
		if namespace == "" {
			namespace = defaultNamespace
		}
		// Still empty: the subject cannot be resolved to a username,
		// and "system:serviceaccount::name" would index an identity
		// that can never authenticate.
		if namespace == "" {
			return graph.Subject{}, false
		}
		return graph.User("system:serviceaccount:" + namespace + ":" + rs.Name), true
	default:
		return graph.Subject{}, false
	}
}

func subjectSet(ss []graph.Subject) map[graph.Subject]struct{} {
	out := make(map[graph.Subject]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
}
