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
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

// IsIdentityAgnostic reports whether a permission claim resolves its identity
// per consumer workspace instead of naming one identity hash. Built-in APIs and
// the apis.kcp.io group carry no identity by construction and are not
// identity-agnostic in this sense; they are handled by their own code paths.
func IsIdentityAgnostic(claim apisv1alpha2.PermissionClaim, isBuiltIn func(apis.GroupResource) bool) bool {
	if claim.IdentityHash != "" {
		return false
	}
	if isBuiltIn != nil && isBuiltIn(claim.GroupResource) {
		return false
	}
	return claim.Group != apisv1alpha2.SchemeGroupVersion.Group
}

// ClaimedIdentity is one identity under which a claimed resource is bound in a
// consumer workspace, together with the binding that established it.
type ClaimedIdentity struct {
	// IdentityHash is the identity hash of the APIExport the consumer bound for
	// the claimed resource.
	IdentityHash string
	// Schema is the bound schema in the consumer workspace: its name, UID, and
	// the identity hash of the export it belongs to.
	Schema apisv1alpha2.BoundAPIResourceSchema
	// Cluster is the consumer workspace.
	Cluster logicalcluster.Name
}

// IdentityResolver resolves, for a claiming APIExport and a claimed
// group/resource, the identities under which that resource is bound in the
// consumer workspaces that accepted the claim. It only sees the APIBindings of
// the shard whose indexer it was built with, which is exactly the set a shard's
// virtual workspace or authorizer needs.
type IdentityResolver struct {
	apiBindingIndexer cache.Indexer
}

// NewIdentityResolver builds a resolver over the given APIBinding indexer. The
// indexer must have indexers.APIBindingsByAPIExport and
// indexers.APIBindingByBoundResources registered.
func NewIdentityResolver(apiBindingIndexer cache.Indexer) *IdentityResolver {
	return &IdentityResolver{apiBindingIndexer: apiBindingIndexer}
}

// Resolve returns the distinct identities under which gr is bound in the
// consumer workspaces on this shard that hold an APIBinding to the claiming
// export with an accepted claim for gr. The result is sorted by identity hash
// so callers get a deterministic order.
//
// A consumer that accepted the claim but has no binding serving gr contributes
// nothing: the claim labeler labels nothing there either, so the resource is
// simply absent for that workspace.
func (r *IdentityResolver) Resolve(export *apisv1alpha2.APIExport, gr schema.GroupResource) ([]ClaimedIdentity, error) {
	bindings, err := r.consumerBindings(export)
	if err != nil {
		return nil, err
	}

	byHash := map[string]ClaimedIdentity{}
	for _, binding := range bindings {
		if !acceptsClaim(binding, gr) {
			continue
		}
		identity, found, err := r.identityInCluster(logicalcluster.From(binding), gr)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if _, seen := byHash[identity.IdentityHash]; !seen {
			byHash[identity.IdentityHash] = identity
		}
	}

	result := make([]ClaimedIdentity, 0, len(byHash))
	for _, identity := range byHash {
		result = append(result, identity)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].IdentityHash < result[j].IdentityHash })
	return result, nil
}

// ResolveInCluster returns the identity under which gr is bound in the given
// consumer workspace, if the workspace holds a binding serving gr. It does not
// check that the workspace accepted a claim; callers that need that gate it
// separately (the bound API authorizer already does).
func (r *IdentityResolver) ResolveInCluster(cluster logicalcluster.Name, gr schema.GroupResource) (ClaimedIdentity, bool, error) {
	return r.identityInCluster(cluster, gr)
}

// IdentityHashes is a convenience over Resolve returning only the hashes.
func (r *IdentityResolver) IdentityHashes(export *apisv1alpha2.APIExport, gr schema.GroupResource) ([]string, error) {
	identities, err := r.Resolve(export, gr)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(identities))
	for _, identity := range identities {
		hashes = append(hashes, identity.IdentityHash)
	}
	return hashes, nil
}

// consumerBindings lists the bindings on this shard that reference the export.
// The by-export index is keyed by the path the binding used, so both the
// canonical path (root:org:provider) and the cluster-name path are queried.
func (r *IdentityResolver) consumerBindings(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
	keys := []string{
		logicalcluster.From(export).Path().Join(export.Name).String(),
	}
	if canonical, ok := export.Annotations[core.LogicalClusterPathAnnotationKey]; ok && canonical != "" {
		keys = append(keys, logicalcluster.NewPath(canonical).Join(export.Name).String())
	}

	seen := map[string]struct{}{}
	var result []*apisv1alpha2.APIBinding
	for _, key := range keys {
		objs, err := r.apiBindingIndexer.ByIndex(indexers.APIBindingsByAPIExport, key)
		if err != nil {
			return nil, fmt.Errorf("error listing APIBindings for export %q: %w", key, err)
		}
		for _, obj := range objs {
			binding, ok := obj.(*apisv1alpha2.APIBinding)
			if !ok {
				return nil, fmt.Errorf("unexpected type %T in APIBinding indexer", obj)
			}
			id := logicalcluster.From(binding).String() + "|" + binding.Name
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, binding)
		}
	}
	return result, nil
}

func (r *IdentityResolver) identityInCluster(cluster logicalcluster.Name, gr schema.GroupResource) (ClaimedIdentity, bool, error) {
	objs, err := r.apiBindingIndexer.ByIndex(indexers.APIBindingByBoundResources, indexers.APIBindingBoundResourceValue(cluster, gr.Group, gr.Resource))
	if err != nil {
		return ClaimedIdentity{}, false, fmt.Errorf("error listing APIBindings bound to %s in %s: %w", gr, cluster, err)
	}
	for _, obj := range objs {
		binding, ok := obj.(*apisv1alpha2.APIBinding)
		if !ok {
			return ClaimedIdentity{}, false, fmt.Errorf("unexpected type %T in APIBinding indexer", obj)
		}
		for _, bound := range binding.Status.BoundResources {
			if bound.Group != gr.Group || bound.Resource != gr.Resource || bound.Schema.IdentityHash == "" {
				continue
			}
			return ClaimedIdentity{
				IdentityHash: bound.Schema.IdentityHash,
				Schema:       bound.Schema,
				Cluster:      cluster,
			}, true, nil
		}
	}
	return ClaimedIdentity{}, false, nil
}

func acceptsClaim(binding *apisv1alpha2.APIBinding, gr schema.GroupResource) bool {
	for _, claim := range binding.Spec.PermissionClaims {
		if claim.State == apisv1alpha2.ClaimAccepted && claim.Group == gr.Group && claim.Resource == gr.Resource && claim.IdentityHash == "" {
			return true
		}
	}
	return false
}
