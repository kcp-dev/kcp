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
	// APIExportEndpointSliceByAPIExport is the indexer name for retrieving APIExportEndpointSlices by their APIExport's Reference Path and Name.
	APIExportEndpointSliceByAPIExport = "APIExportEndpointSliceByAPIExport"

	APIExportByVirtualResourceFingerprint = "APIExportByVirtualResourceFingerprint"

	// APIExportByAPIResourceSchema is the indexer name for retrieving APIExports by the
	// cluster-aware key of one of their APIResourceSchemas (Spec.Resources[].Schema).
	APIExportByAPIResourceSchema = "apiExportsByAPIResourceSchema"
)

// IndexAPIExportByAPIResourceSchema is an index function that maps an APIExport to the
// cluster-aware keys of every APIResourceSchema it references in Spec.Resources. The key
// format is "<schemaCluster>|<schemaName>", matching client.ToClusterAwareKey output.
func IndexAPIExportByAPIResourceSchema(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}
	cluster := logicalcluster.From(apiExport).Path()
	ret := make([]string, 0, len(apiExport.Spec.Resources))
	for _, resourceSchema := range apiExport.Spec.Resources {
		ret = append(ret, sdkclient.ToClusterAwareKey(cluster, resourceSchema.Schema))
	}
	return ret, nil
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
func CanonicalIdentityHash(local, global cache.Indexer, hash string) string {
	if hash == "" {
		return hash
	}
	exports, err := ByIndexWithFallback[*apisv1alpha2.APIExport](local, global, APIExportByIdentity, hash)
	if err != nil || len(exports) != 1 {
		return hash
	}
	if canonical := exports[0].Status.IdentityHash; canonical != "" {
		return canonical
	}
	return hash
}

// IndexAPIExportBySecret is an index function that indexes an APIExport by its identity secret references. Index values
// are of the form <cluster name>|<secret reference namespace>/<secret reference name> (cache keys).
func IndexAPIExportBySecret(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha2.APIExport)

	if apiExport.Spec.Identity == nil {
		return []string{}, nil
	}

	ref := apiExport.Spec.Identity.SecretRef
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
	for _, claim := range apiExport.Spec.PermissionClaims {
		claimedIdentities.Insert(claim.IdentityHash)
	}
	return sets.List[string](claimedIdentities), nil
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
