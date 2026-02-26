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

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
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

	APIExportByVirtualResourceIdentities       = "APIExportByVirtualResourceIdentities"
	APIExportByVirtualResourceIdentitiesAndGRs = "APIExportByVirtualResourceIdentitiesAndGRs"
)

// IndexAPIExportByIdentity is an index function that indexes an APIExport by its identity hash.
func IndexAPIExportByIdentity(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha2.APIExport)
	return []string{apiExport.Status.IdentityHash}, nil
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

// IndexAPIExportByVirtualResourceIdentities is an index function that indexes an APIExport by its
// exported resources' virtual storage identity.
func IndexAPIExportByVirtualResourceIdentities(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}

	virtualResourceIdentities := sets.New[string]()

	for _, res := range apiExport.Spec.Resources {
		if res.Storage.Virtual != nil {
			virtualResourceIdentities.Insert(res.Storage.Virtual.IdentityHash)
		}
	}

	return sets.List[string](virtualResourceIdentities), nil
}

// IndexAPIExportByVirtualResourceIdentities is an index function that indexes an APIExport by its
// exported resources' virtual storage identity and group resource.
func IndexAPIExportByVirtualResourceIdentitiesAndGRs(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}

	keys := sets.New[string]()

	for _, res := range apiExport.Spec.Resources {
		if res.Storage.Virtual != nil {
			keys.Insert(VirtualResourceIdentityAndGRKey(res.Storage.Virtual.IdentityHash, schema.GroupResource{
				Group:    res.Group,
				Resource: res.Name,
			}))
		}
	}

	return sets.List[string](keys), nil
}

func VirtualResourceIdentityAndGRKey(identityHash string, gr schema.GroupResource) string {
	return fmt.Sprintf("%s:%s", gr.String(), identityHash)
}
