/*
Copyright 2022 The kcp Authors.

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

package indexers

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	sdkclient "github.com/kcp-dev/sdk/client"

	vrhelpers "github.com/kcp-dev/kcp/pkg/server/virtualresources/helpers"
)

const (
	// APIExportByIdentity is the indexer name for retrieving APIExports by identity hash.
	APIExportByIdentity = "APIExportByIdentity"
	// APIExportBySecret is the indexer name for retrieving APIExports by secret.
	APIExportBySecret = "APIExportSecret"
	// APIExportByClaimedIdentities is the indexer name for retrieving APIExports that have a permission claim for a
	// particular identity hash.
	APIExportByClaimedIdentities = "APIExportByClaimedIdentities"
	// APIExportByStorageKey is the indexer name for retrieving APIExports by
	// the storage prefixes their resources are reported to use
	// (status.storage.resources[].keys): the export's own identity, or the
	// source's identity for an adopted resource.
	APIExportByStorageKey = "APIExportByStorageKey"
	// APIExportEndpointSliceByAPIExport is the indexer name for retrieving APIExportEndpointSlices by their APIExport's Reference Path and Name.
	APIExportEndpointSliceByAPIExport = "APIExportEndpointSliceByAPIExport"

	APIExportByVirtualResourceFingerprint = "APIExportByVirtualResourceFingerprint"

	// APIExportByAPIResourceSchema is the indexer name for retrieving APIExports by the
	// cluster-aware key of one of their APIResourceSchemas (Spec.Resources[].Schema).
	APIExportByAPIResourceSchema = "apiExportsByAPIResourceSchema"

	// APIExportByClaimGrant is the indexer name for retrieving APIExports that
	// grant a claim on one of their resources: by explicit grantee
	// ("export|<cluster path or name>|<name>|<resource>.<group>"), by selector
	// ("selector|<resource>.<group>") or through RBAC ("rbac|<resource>.<group>").
	APIExportByClaimGrant = "apiExportsByClaimGrant"
)

// ClaimGrantExportKey is the APIExportByClaimGrant key of an explicit grant to
// the export at path (or cluster name) with name for the group/resource.
func ClaimGrantExportKey(path, name, group, resource string) string {
	return "export|" + path + "|" + name + "|" + resource + "." + group
}

// ClaimGrantSelectorKey is the APIExportByClaimGrant key of all selector
// grants for the group/resource.
func ClaimGrantSelectorKey(group, resource string) string {
	return "selector|" + resource + "." + group
}

// ClaimGrantRBACKey is the APIExportByClaimGrant key of all RBAC grants for
// the group/resource.
func ClaimGrantRBACKey(group, resource string) string {
	return "rbac|" + resource + "." + group
}

// IndexAPIExportByClaimGrant indexes an APIExport by the claim grants on its
// resources (spec.resources[].claimableBy).
func IndexAPIExportByClaimGrant(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}
	keys := sets.New[string]()
	for _, rs := range apiExport.Spec.Resources {
		for _, grant := range rs.ClaimableBy {
			switch {
			case grant.Export != nil:
				keys.Insert(ClaimGrantExportKey(grant.Export.Path, grant.Export.Name, rs.Group, rs.Name))
			case grant.ExportSelector != nil:
				keys.Insert(ClaimGrantSelectorKey(rs.Group, rs.Name))
			case grant.RBAC != nil:
				keys.Insert(ClaimGrantRBACKey(rs.Group, rs.Name))
			}
		}
	}
	return sets.List(keys), nil
}

// IndexAPIExportByAPIResourceSchema is an index function that maps an APIExport to the
// cluster-aware keys of every APIResourceSchema it references in Spec.Resources. The key
// format is "<schemaCluster>|<schemaName>", matching client.ToClusterAwareKey output.
func IndexAPIExportByAPIResourceSchema(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}
	cluster := logicalcluster.From(apiExport).Path()
	keys := sets.New[string]()
	for _, resourceSchema := range apiExport.Spec.Resources {
		if resourceSchema.Schema != "" {
			keys.Insert(sdkclient.ToClusterAwareKey(cluster, resourceSchema.Schema))
		}
	}
	// an adopted resource names no schema of its own; the one in effect (the
	// source export's, in the source's cluster) is recorded in status.
	for _, status := range apiExport.Status.Resources {
		if status.Schema == "" {
			continue
		}
		schemaCluster := cluster
		if status.SchemaCluster != "" {
			schemaCluster = logicalcluster.NewPath(status.SchemaCluster)
		}
		keys.Insert(sdkclient.ToClusterAwareKey(schemaCluster, status.Schema))
	}
	return sets.List(keys), nil
}

// IndexAPIExportByIdentity is an index function that indexes an APIExport by its identity hash.
func IndexAPIExportByIdentity(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha2.APIExport)
	// alias hashes of an in-progress identity rotation resolve to the same
	// export, so permission claims and wildcard consumers referencing a
	// pre-rotation identity keep working until the alias is retired.
	return append([]string{apiExport.Status.IdentityHash}, apiExport.Status.IdentityAliasHashes...), nil
}

// CanonicalIdentityHash normalizes an identity hash to its canonical value:
// if the hash is an alias of exactly one rotated APIExport, that export's
// current identity hash is returned; otherwise the hash itself. Everything
// that hashes permission claims must normalize through this first, so a
// claim still referencing a pre-rotation identity and one already updated
// produce identical label keys and values - otherwise claimed objects
// silently disappear from the claiming export's view.
//
// An ambiguous hash (several exports resolve to it, i.e. a shared identity)
// is returned unchanged: normalization is only safe when the alias maps to
// one export.
//
// The indexer must hold the global (cache server) view of APIExports: a
// shard-local view could see only one of several exports sharing the hash
// and normalize where other shards and the virtual workspace do not.
// This is why we use only the global indexer for canonicalization, not any shard-local caches.
func CanonicalIdentityHash(globalIndexer cache.Indexer, hash string) string {
	if hash == "" {
		return hash
	}
	exports, err := ByIndex[*apisv1alpha2.APIExport](globalIndexer, APIExportByIdentity, hash)
	if err != nil || len(exports) != 1 {
		return hash
	}
	if canonical := exports[0].Status.IdentityHash; canonical != "" {
		return canonical
	}
	return hash
}

// CanonicalIdentityHashForGR normalizes an identity hash for one
// group/resource. Aliases are scoped: when a legacy shared identity X was
// converted, the owner keeps X and every other former holder Ei gets a fresh
// identity Yi with X as an alias, so X resolves to several exports. For a
// claim on {gr, X} the canonical identity is that of the export that serves
// gr: Yi if Ei serves it, X if the owner does. With exactly one export
// resolving the hash this is CanonicalIdentityHash; with none, or with
// several serving gr, the hash is returned unchanged.
func CanonicalIdentityHashForGR(globalIndexer cache.Indexer, hash, group, resource string) string {
	if hash == "" {
		return hash
	}
	exports, err := ByIndex[*apisv1alpha2.APIExport](globalIndexer, APIExportByIdentity, hash)
	if err != nil || len(exports) == 0 {
		return hash
	}
	if len(exports) == 1 {
		if canonical := exports[0].Status.IdentityHash; canonical != "" {
			return canonical
		}
		return hash
	}
	canonical := ""
	for _, export := range exports {
		if export.Status.IdentityHash == "" || !ExportServesGR(export, group, resource) {
			continue
		}
		if canonical != "" && canonical != export.Status.IdentityHash {
			return hash // ambiguous
		}
		canonical = export.Status.IdentityHash
	}
	if canonical == "" {
		return hash
	}
	return canonical
}

// ExportServesGR reports whether the export serves the group/resource.
func ExportServesGR(export *apisv1alpha2.APIExport, group, resource string) bool {
	for _, rs := range export.Spec.Resources {
		if rs.Group == group && rs.Name == resource {
			return true
		}
	}
	return false
}

// IndexAPIExportBySecret is an index function that indexes an APIExport by its identity secret references. Index values
// are of the form <cluster name>|<secret reference namespace>/<secret reference name> (cache keys).
func IndexAPIExportBySecret(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha2.APIExport)

	if apiExport.Spec.Identity == nil {
		return []string{}, nil
	}

	ref := apiExport.Spec.Identity.SecretRef //nolint:staticcheck // secretRef is honored during its deprecation window
	if ref == nil {
		return []string{}, nil
	}

	if ref.Namespace == "" || ref.Name == "" {
		return []string{}, nil
	}

	return []string{kcpcache.ToClusterAwareKey(logicalcluster.From(apiExport).String(), ref.Namespace, ref.Name)}, nil
}

// IndexAPIExportByClaimedIdentities is an index function that indexes an APIExport by its permission claims' identity
// hashes.
func IndexAPIExportByClaimedIdentities(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha2.APIExport)
	claimedIdentities := sets.New[string]()
	// resolved claims: a claim naming an export or resolved through a grant
	// carries its identity in status only.
	for _, claim := range apiExport.ResolvedPermissionClaims() {
		claimedIdentities.Insert(claim.IdentityHash)
	}
	return sets.List[string](claimedIdentities), nil
}

// IndexAPIExportByStorageKey indexes an APIExport by every storage prefix its
// resources are reported to use.
func IndexAPIExportByStorageKey(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}
	if apiExport.Status.Storage == nil {
		return []string{}, nil
	}
	keys := sets.New[string]()
	for _, rs := range apiExport.Status.Storage.Resources {
		keys.Insert(rs.Keys...)
	}
	return sets.List(keys), nil
}

// IndexAPIExportEndpointSliceByAPIExportFunc indexes the APIExportEndpointSlice by their APIExport's Reference Path and Name.
func IndexAPIExportEndpointSliceByAPIExport(obj interface{}) ([]string, error) {
	apiExportEndpointSlice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExportEndpointSlice", obj)
	}

	var result []string
	pathRemote := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if !pathRemote.Empty() {
		result = append(result, pathRemote.Join(apiExportEndpointSlice.Spec.APIExport.Name).String())
	}
	pathLocal := logicalcluster.From(apiExportEndpointSlice).Path()
	if !pathLocal.Empty() {
		result = append(result, pathLocal.Join(apiExportEndpointSlice.Spec.APIExport.Name).String())
	}

	return result, nil
}

// IndexAPIExportByVirtualResourceFingerprint indexes an APIExport by the fingerprint of each
// virtual storage resource (owning APIExport + endpoint slice reference).
func IndexAPIExportByVirtualResourceFingerprint(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}

	keys := sets.New[string]()
	for _, res := range apiExport.Spec.Resources {
		if res.Storage.Virtual != nil {
			keys.Insert(vrhelpers.Fingerprint(apiExport, res.Storage.Virtual))
		}
	}

	return sets.List(keys), nil
}
