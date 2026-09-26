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

package permissionclaim

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

var cowboys = schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

func TestIsIdentityAgnostic(t *testing.T) {
	t.Parallel()

	isBuiltIn := func(gr apis.GroupResource) bool {
		return gr.GetGroup() == "" || gr.GetGroup() == "apps"
	}

	for _, tc := range []struct {
		name      string
		claim     apisv1alpha2.PermissionClaim
		isBuiltIn func(apis.GroupResource) bool
		want      bool
	}{
		{
			name: "no identity hash on a third party group is identity-agnostic",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"},
			},
			isBuiltIn: isBuiltIn,
			want:      true,
		},
		{
			name: "identity hash set is never identity-agnostic",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"},
				IdentityHash:  "abcdef",
			},
			isBuiltIn: isBuiltIn,
			want:      false,
		},
		{
			name: "built-in core group is not identity-agnostic",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
			},
			isBuiltIn: isBuiltIn,
			want:      false,
		},
		{
			name: "built-in non-core group is not identity-agnostic",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "apps", Resource: "deployments"},
			},
			isBuiltIn: isBuiltIn,
			want:      false,
		},
		{
			name: "apis.kcp.io is not identity-agnostic",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: apisv1alpha2.SchemeGroupVersion.Group, Resource: "apibindings"},
			},
			isBuiltIn: isBuiltIn,
			want:      false,
		},
		{
			name: "nil isBuiltIn func is tolerated",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"},
			},
			isBuiltIn: nil,
			want:      true,
		},
		{
			name: "nil isBuiltIn func still excludes apis.kcp.io",
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: apisv1alpha2.SchemeGroupVersion.Group, Resource: "apiexports"},
			},
			isBuiltIn: nil,
			want:      false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsIdentityAgnostic(tc.claim, tc.isBuiltIn))
		})
	}
}

// newAPIBindingIndexer builds an indexer over cluster-aware APIBindings with
// exactly the indexers NewIdentityResolver requires.
func newAPIBindingIndexer(t *testing.T, bindings ...*apisv1alpha2.APIBinding) cache.Indexer {
	t.Helper()

	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		indexers.APIBindingsByAPIExport:     indexers.IndexAPIBindingByAPIExport,
		indexers.APIBindingByBoundResources: indexers.IndexAPIBindingByBoundResources,
	})
	for _, binding := range bindings {
		require.NoError(t, indexer.Add(binding))
	}
	return indexer
}

type bindingOption func(*apisv1alpha2.APIBinding)

// withAcceptedClaim adds an accepted permission claim for gr with the given
// identity hash (empty for an identity-agnostic claim).
func withAcceptedClaim(gr schema.GroupResource, identityHash string) bindingOption {
	return func(binding *apisv1alpha2.APIBinding) {
		binding.Spec.PermissionClaims = append(binding.Spec.PermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: gr.Group, Resource: gr.Resource},
					Verbs:         []string{"*"},
					IdentityHash:  identityHash,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}
}

// withRejectedClaim adds a rejected identity-agnostic claim for gr.
func withRejectedClaim(gr schema.GroupResource) bindingOption {
	return func(binding *apisv1alpha2.APIBinding) {
		binding.Spec.PermissionClaims = append(binding.Spec.PermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: gr.Group, Resource: gr.Resource},
					Verbs:         []string{"*"},
				},
				Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
			},
			State: apisv1alpha2.ClaimRejected,
		})
	}
}

// withBoundResource records gr as bound under the given identity hash.
func withBoundResource(gr schema.GroupResource, identityHash string) bindingOption {
	return func(binding *apisv1alpha2.APIBinding) {
		binding.Status.BoundResources = append(binding.Status.BoundResources, apisv1alpha2.BoundAPIResource{
			Group:    gr.Group,
			Resource: gr.Resource,
			Schema: apisv1alpha2.BoundAPIResourceSchema{
				Name:         gr.Resource + "." + gr.Group,
				UID:          "uid-" + identityHash,
				IdentityHash: identityHash,
			},
		})
	}
}

func newAPIBinding(cluster logicalcluster.Name, name, exportPath, exportName string, opts ...bindingOption) *apisv1alpha2.APIBinding {
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath,
					Name: exportName,
				},
			},
		},
	}
	for _, opt := range opts {
		opt(binding)
	}
	return binding
}

func newAPIExport(cluster logicalcluster.Name, name, canonicalPath string) *apisv1alpha2.APIExport {
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
	}
	if canonicalPath != "" {
		export.Annotations[core.LogicalClusterPathAnnotationKey] = canonicalPath
	}
	return export
}

func TestIdentityResolverResolve(t *testing.T) {
	t.Parallel()

	const (
		claimingCluster = "claimingws"
		claimingExport  = "claiming-export"

		identityA = "aaaa1111"
		identityB = "bbbb2222"
	)

	consumerA := logicalcluster.Name("consumera")
	consumerB := logicalcluster.Name("consumerb")

	for _, tc := range []struct {
		name     string
		export   *apisv1alpha2.APIExport
		bindings []*apisv1alpha2.APIBinding
		want     []ClaimedIdentity
	}{
		{
			name:   "two consumers binding different producers yield both identities, sorted",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				// consumer-b first, and the higher hash first, to prove sorting is not accidental.
				newAPIBinding(consumerB, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerB, "cowboys", "root:org:provider-b", "cowboys", withBoundResource(cowboys, identityB)),
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: []ClaimedIdentity{
				{IdentityHash: identityA, Cluster: consumerA},
				{IdentityHash: identityB, Cluster: consumerB},
			},
		},
		{
			name:   "two consumers binding the same producer yield one identity",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
				newAPIBinding(consumerB, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerB, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: []ClaimedIdentity{
				{IdentityHash: identityA, Cluster: consumerA},
			},
		},
		{
			name:   "consumer accepted the claim but binds nothing serving the resource",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				// binds a different group/resource entirely
				newAPIBinding(consumerA, "sheriffs", "root:org:other", "sheriffs",
					withBoundResource(schema.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"}, "cccc3333")),
			},
			want: nil,
		},
		{
			name:   "consumer binds the resource but did not accept the claim",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withRejectedClaim(cowboys)),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: nil,
		},
		{
			name:   "consumer does not bind the claiming export at all",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: nil,
		},
		{
			name:   "accepted claim carrying an identity hash is not identity-agnostic",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, identityA)),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: nil,
		},
		{
			name:   "export reference resolves through both the cluster name and the canonical path",
			export: newAPIExport(claimingCluster, claimingExport, "root:org:claiming"),
			bindings: []*apisv1alpha2.APIBinding{
				// consumer-a references the export by cluster name ...
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
				// ... consumer-b by the canonical kcp.io/path.
				newAPIBinding(consumerB, "claiming", "root:org:claiming", claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerB, "cowboys", "root:org:provider-b", "cowboys", withBoundResource(cowboys, identityB)),
			},
			want: []ClaimedIdentity{
				{IdentityHash: identityA, Cluster: consumerA},
				{IdentityHash: identityB, Cluster: consumerB},
			},
		},
		{
			name:   "a binding matching both keys is only counted once",
			export: newAPIExport(claimingCluster, claimingExport, claimingCluster),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, identityA)),
			},
			want: []ClaimedIdentity{
				{IdentityHash: identityA, Cluster: consumerA},
			},
		},
		{
			name:   "bound resource without an identity hash contributes nothing",
			export: newAPIExport(claimingCluster, claimingExport, ""),
			bindings: []*apisv1alpha2.APIBinding{
				newAPIBinding(consumerA, "claiming", claimingCluster, claimingExport, withAcceptedClaim(cowboys, "")),
				newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, "")),
			},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolver := NewIdentityResolver(newAPIBindingIndexer(t, tc.bindings...))

			got, err := resolver.Resolve(tc.export, cowboys)
			require.NoError(t, err)

			require.Len(t, got, len(tc.want))
			for i := range tc.want {
				require.Equal(t, tc.want[i].IdentityHash, got[i].IdentityHash, "identity hash at %d", i)
				require.Equal(t, tc.want[i].Cluster, got[i].Cluster, "cluster at %d", i)
				require.Equal(t, tc.want[i].IdentityHash, got[i].Schema.IdentityHash, "schema identity hash at %d", i)
			}

			// IdentityHashes must agree with Resolve, in the same order.
			hashes, err := resolver.IdentityHashes(tc.export, cowboys)
			require.NoError(t, err)
			wantHashes := make([]string, 0, len(tc.want))
			for _, identity := range tc.want {
				wantHashes = append(wantHashes, identity.IdentityHash)
			}
			require.Equal(t, wantHashes, hashes)
		})
	}
}

func TestIdentityResolverResolveInCluster(t *testing.T) {
	t.Parallel()

	consumerA := logicalcluster.Name("consumera")
	consumerB := logicalcluster.Name("consumerb")

	indexer := newAPIBindingIndexer(t,
		newAPIBinding(consumerA, "cowboys", "root:org:provider-a", "cowboys", withBoundResource(cowboys, "aaaa1111")),
		newAPIBinding(consumerB, "cowboys", "root:org:provider-b", "cowboys", withBoundResource(cowboys, "bbbb2222")),
	)
	resolver := NewIdentityResolver(indexer)

	t.Run("known cluster", func(t *testing.T) {
		t.Parallel()
		identity, found, err := resolver.ResolveInCluster(consumerA, cowboys)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "aaaa1111", identity.IdentityHash)
		require.Equal(t, consumerA, identity.Cluster)
		require.Equal(t, "cowboys.wildwest.dev", identity.Schema.Name)
	})

	t.Run("other cluster resolves to its own identity", func(t *testing.T) {
		t.Parallel()
		identity, found, err := resolver.ResolveInCluster(consumerB, cowboys)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "bbbb2222", identity.IdentityHash)
	})

	t.Run("unknown cluster", func(t *testing.T) {
		t.Parallel()
		identity, found, err := resolver.ResolveInCluster(logicalcluster.Name("nobodyhome"), cowboys)
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, identity.IdentityHash)
	})

	t.Run("known cluster, unknown resource", func(t *testing.T) {
		t.Parallel()
		_, found, err := resolver.ResolveInCluster(consumerA, schema.GroupResource{Group: "wildwest.dev", Resource: "sheriffs"})
		require.NoError(t, err)
		require.False(t, found)
	})
}
