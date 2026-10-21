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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	APIBindingByIdentityAndGroupResource = "apibinding-byIdentityGroupResource"
)

// IndexAPIBindingByIdentityGroupResource is an index function that indexes an APIBinding by its
// bound resources' identity and group resource.
func IndexAPIBindingByIdentityGroupResource(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIBinding, but is %T", obj)
	}

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))

	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, IdentityGroupResourceKeyFunc(r.Schema.IdentityHash, r.Group, r.Resource))
	}

	return ret, nil
}

func IdentityGroupResourceKeyFunc(identity, group, resource string) string {
	return fmt.Sprintf("%s/%s/%s", identity, group, resource)
}

// ClusterAndGroupResourceValue returns the index value for use with
// IndexAPIBindingByClusterAndAcceptedClaimedGroupResources from clusterName and groupResource.
func ClusterAndGroupResourceValue(clusterName logicalcluster.Name, groupResource schema.GroupResource) string {
	return fmt.Sprintf("%s|%s", clusterName, groupResource)
}

// IndexAPIBindingByClusterAndAcceptedClaimedGroupResources is an index function that indexes an APIBinding by its
// accepted permission claims' group resources.
func IndexAPIBindingByClusterAndAcceptedClaimedGroupResources(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Spec.PermissionClaims))
	for _, c := range apiBinding.Spec.PermissionClaims {
		if c.State != apisv1alpha2.ClaimAccepted {
			continue
		}

		groupResource := schema.GroupResource{Group: c.Group, Resource: c.Resource}
		ret = append(ret, ClusterAndGroupResourceValue(logicalcluster.From(apiBinding), groupResource))
	}

	return ret, nil
}

// APIBindingByAcceptedClaimedGroupResource is the indexer name for retrieving APIBindings by
// the (group, resource) of any of their accepted permission claims, regardless of cluster.
const APIBindingByAcceptedClaimedGroupResource = "byAcceptedClaimedGroupResource"

// IndexAPIBindingByAcceptedClaimedGroupResource is an index function that indexes an APIBinding
// by every accepted permission claim's group/resource, with no cluster prefix. Used to find
// every binding on a shard that claims a given GVR (e.g. when a CRD's informer is added or
// removed shard-wide).
func IndexAPIBindingByAcceptedClaimedGroupResource(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Spec.PermissionClaims))
	for _, c := range apiBinding.Spec.PermissionClaims {
		if c.State != apisv1alpha2.ClaimAccepted {
			continue
		}
		ret = append(ret, schema.GroupResource{Group: c.Group, Resource: c.Resource}.String())
	}

	return ret, nil
}

// ListAPIBindingsByAcceptedClaimedGroupResource returns every APIBinding on the shard that has
// an accepted permission claim for the given group/resource. The indexer must have
// APIBindingByAcceptedClaimedGroupResource registered.
func ListAPIBindingsByAcceptedClaimedGroupResource(indexer cache.Indexer, gr schema.GroupResource) ([]*apisv1alpha2.APIBinding, error) {
	return ByIndex[*apisv1alpha2.APIBinding](indexer, APIBindingByAcceptedClaimedGroupResource, gr.String())
}

// APIBindingByBoundResourceUID is the indexer name for retrieving APIBindings
// by the names of the bound CRDs their bound resources are served from. The
// name is the schema UID for a resource stored under the export's identity,
// and a per-storage-key variant otherwise (see BoundCRDNamesFor); every
// storage key the resource may still have instances under is indexed, so a
// bound CRD stays referenced while a drain is in progress.
const APIBindingByBoundResourceUID = "byBoundResourceUID"

func IndexAPIBindingByBoundResourceUID(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))
	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, BoundCRDNamesFor(r)...)
	}

	return ret, nil
}

// BoundCRDNameForKey returns the name of the bound CRD in system:bound-crds
// that serves a resource of the given schema from the given storage key. A
// resource stored under its export's identity, the steady state, uses the
// schema UID as it always has. A resource on any other prefix (adopted
// storage, or a prefix it is being re-keyed onto) uses a per-key variant, so
// that every storage key gets a bound CRD object of its own and therefore its
// own serving storage on a shard: the apiextensions server caches the etcd
// prefix per CRD object.
func BoundCRDNameForKey(schemaUID, identityHash, storageKey string) string {
	if storageKey == "" || storageKey == identityHash {
		return schemaUID
	}
	sum := sha256.Sum256([]byte(storageKey))
	return schemaUID + "-" + hex.EncodeToString(sum[:])[:12]
}

// BoundCRDNameFor returns the name of the bound CRD a bound resource is
// currently served from.
func BoundCRDNameFor(r apisv1alpha2.BoundAPIResource) string {
	return BoundCRDNameForKey(r.Schema.UID, r.Schema.IdentityHash, r.StorageKey())
}

// BoundCRDNamesFor returns the names of every bound CRD a bound resource may
// be served from: the one for its current storage key and one for every
// pending key.
func BoundCRDNamesFor(r apisv1alpha2.BoundAPIResource) []string {
	names := []string{BoundCRDNameFor(r)}
	for _, key := range r.Storage.PendingKeys {
		if name := BoundCRDNameForKey(r.Schema.UID, r.Schema.IdentityHash, key); name != names[0] {
			names = append(names, name)
		}
	}
	return names
}

// APIBindingByAcceptedClaimIdentityAndGR is the indexer name for retrieving APIBindings by
// the (identityHash, group, resource) of any of their accepted permission claims that
// reference an external APIExport (i.e. claims with a non-empty IdentityHash).
const APIBindingByAcceptedClaimIdentityAndGR = "apibinding-byAcceptedClaimIdentityAndGR"

// IndexAPIBindingByAcceptedClaimIdentityAndGR is an index function that indexes an
// APIBinding by every accepted permission claim that targets a resource from an external
// APIExport. The index key is "<identityHash>/<group>/<resource>".
//
// CRDCleanup uses this to ask: "which APIBindings still need a particular bound CRD via a
// permission claim?" The CRD's group+resource come from its spec; the producer's identity
// hash comes from the APIExport that owns the schema annotated on the CRD.
func IndexAPIBindingByAcceptedClaimIdentityAndGR(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Spec.PermissionClaims))
	for _, c := range apiBinding.Spec.PermissionClaims {
		if c.State != apisv1alpha2.ClaimAccepted {
			continue
		}
		if c.IdentityHash == "" {
			continue
		}
		ret = append(ret, IdentityGroupResourceKeyFunc(c.IdentityHash, c.Group, c.Resource))
	}
	return ret, nil
}

// APIBindingByStorageKey is the indexer name for retrieving APIBindings by
// the storage prefixes their bound resources store under (current and
// pending keys).
const APIBindingByStorageKey = "byStorageKey"

// IndexAPIBindingByStorageKey indexes an APIBinding by every storage prefix
// one of its bound resources stores or may still store under.
func IndexAPIBindingByStorageKey(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}
	keys := map[string]struct{}{}
	for _, r := range apiBinding.Status.BoundResources {
		if key := r.StorageKey(); key != "" {
			keys[key] = struct{}{}
		}
		for _, key := range r.Storage.PendingKeys {
			keys[key] = struct{}{}
		}
	}
	ret := make([]string, 0, len(keys))
	for key := range keys {
		ret = append(ret, key)
	}
	return ret, nil
}

const APIBindingByBoundResources = "byBoundResources"

func IndexAPIBindingByBoundResources(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	clusterName := logicalcluster.From(apiBinding)

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))
	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, APIBindingBoundResourceValue(clusterName, r.Group, r.Resource))
	}

	return ret, nil
}

func APIBindingBoundResourceValue(clusterName logicalcluster.Name, group, resource string) string {
	return fmt.Sprintf("%s|%s.%s", clusterName, resource, group)
}

const APIBindingsByAPIExport = "APIBindingByAPIExport"

// IndexAPIBindingByAPIExport indexes the APIBindings by their APIExport's Reference Path and Name.
func IndexAPIBindingByAPIExport(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = logicalcluster.From(apiBinding).Path()
	}

	return []string{path.Join(apiBinding.Spec.Reference.Export.Name).String()}, nil
}
