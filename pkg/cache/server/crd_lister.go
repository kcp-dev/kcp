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

package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/martinlindhe/base36"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	kcpapiextensionsinformers "github.com/kcp-dev/client-go/apiextensions/informers"
	kcpapiextensionsv1listers "github.com/kcp-dev/client-go/apiextensions/listers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	cache1alpha1listers "github.com/kcp-dev/sdk/client/listers/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources"
	kcpfilters "github.com/kcp-dev/kcp/pkg/server/filters"
)

var systemCachedCRDLogicalCluster = logicalcluster.Name("system:cached-crds")

// crdClusterLister is a CRD lister for the built-in APIs as well as
// synthetic ones for CachedResources.
type crdClusterLister struct {
	crdLister  kcpapiextensionsv1listers.CustomResourceDefinitionClusterLister
	crdIndexer cache.Indexer

	cachedResourcesLister  cache1alpha1listers.CachedResourceClusterLister
	cachedResourcesIndexer cache.Indexer

	listCachedResourcesByIdentityAndGR func(identity string, gr schema.GroupResource) ([]*cachev1alpha1.CachedResource, error)
	listCachedResourcesByGR            func(gr schema.GroupResource) ([]*cachev1alpha1.CachedResource, error)
}

func newCacheResourceAwareCRDClusterLister(apiExtensionsSharedInformerFactory kcpapiextensionsinformers.SharedInformerFactory, kcpSharedInformerFactory kcpinformers.SharedInformerFactory) *crdClusterLister {
	return &crdClusterLister{
		crdLister:  apiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer: apiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),

		cachedResourcesLister:  kcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Lister(),
		cachedResourcesIndexer: kcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),

		listCachedResourcesByIdentityAndGR: func(identity string, gr schema.GroupResource) ([]*cachev1alpha1.CachedResource, error) {
			return indexers.ByIndex[*cachev1alpha1.CachedResource](
				kcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),
				cachedresources.ByIdentityAndGroupResource,
				cachedresources.IdentityAndGroupResourceKey(identity, gr),
			)
		},

		listCachedResourcesByGR: func(gr schema.GroupResource) ([]*cachev1alpha1.CachedResource, error) {
			return indexers.ByIndex[*cachev1alpha1.CachedResource](
				kcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),
				cachedresources.ByGroupResource,
				cachedresources.GroupResourceKey(gr),
			)
		},
	}
}

func (c *crdClusterLister) Cluster(cluster logicalcluster.Name) kcp.ClusterAwareCRDLister {
	return &crdLister{
		crdClusterLister: c,
		cluster:          cluster,
	}
}

var _ kcp.ClusterAwareCRDClusterLister = &crdClusterLister{}

// crdLister is a CRD lister.
type crdLister struct {
	*crdClusterLister
	cluster logicalcluster.Name
}

var _ kcp.ClusterAwareCRDLister = &crdLister{}

// shallowCopyCRDAndDeepCopyAnnotations makes a shallow copy of in, with a deep copy of in.ObjectMeta.Annotations.
func shallowCopyCRDAndDeepCopyAnnotations(in *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	out := *in

	out.Annotations = make(map[string]string, len(in.Annotations))
	for k, v := range in.Annotations {
		out.Annotations[k] = v
	}

	return &out
}

// List lists all CustomResourceDefinitions in a cluster.
func (c *crdLister) List(ctx context.Context, selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	canonicalCRDName := func(crd *apiextensionsv1.CustomResourceDefinition) string {
		return crd.Spec.Names.Plural + "." + crd.Spec.Group
	}
	clusterName := c.cluster

	// seen keeps track of which CRDs have already been found from system and CachedResources.
	seen := sets.New[string]()

	// Priority 1: add system CRDs. These take priority over CRDs from CachedResources.
	systemCRDs, err := c.crdLister.Cluster(bootstrap.SystemCRDLogicalCluster).List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("error retrieving kcp system CRDs: %w", err)
	}

	listedCRDs := make([]*apiextensionsv1.CustomResourceDefinition, 0, len(systemCRDs))
	for _, crd := range systemCRDs {
		listedCRDs = append(listedCRDs, crd)
		seen.Insert(canonicalCRDName(crd))
	}

	// Priority 2: add CachedResource CRDs.
	localCachedResources, err := c.cachedResourcesLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, cr := range localCachedResources {
		if cr.Annotations == nil ||
			cr.Annotations[cachedresources.AnnotationResourceKind] == "" ||
			cr.Annotations[cachedresources.AnnotationResourceScope] == "" ||
			cr.Status.IdentityHash == "" {
			continue
		}
		crs, err := c.listCachedResourcesByIdentityAndGR(cr.Status.IdentityHash, schema.GroupVersionResource(cr.Spec.GroupVersionResource).GroupResource())
		if err != nil {
			return nil, err
		}
		crd, err := c.synthesizeCRDForCachedResources(crs)
		if err != nil {
			continue
		}
		if !seen.Has(canonicalCRDName(crd)) {
			listedCRDs = append(listedCRDs, crd)
			seen.Insert(canonicalCRDName(crd))
		}
	}

	return listedCRDs, nil
}

const annotationKeyPartialMetadata = "crd.kcp.io/partial-metadata"

// addPartialMetadataCRDAnnotation adds an annotation that marks this CRD as being
// for a partial metadata request.
func addPartialMetadataCRDAnnotation(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.Annotations[annotationKeyPartialMetadata] = ""
}

func (c *crdLister) Refresh(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	return crd, nil
}

func (c *crdLister) getSystemCRD(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.Cluster(bootstrap.SystemCRDLogicalCluster).Get(name)
}

// Get gets a CustomResourceDefinition.
func (c *crdLister) Get(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	var (
		crd *apiextensionsv1.CustomResourceDefinition
		err error
	)

	clusterName := c.cluster

	// Priority 1: system CRD
	crd, err = c.getSystemCRD(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	partialMetadataRequest := kcpfilters.IsPartialMetadataRequest(ctx)

	if crd == nil {
		// Not a system CRD, so check in priority order: identity, wildcard, "normal" single cluster

		identity := kcpfilters.IdentityFromContext(ctx)

		if clusterName == "*" && identity != "" {
			// Priority 2: APIBinding CRD
			crd, err = c.getForIdentityWildcard(ctx, name, identity)
		} else if clusterName == "*" && partialMetadataRequest {
			// Priority 3: partial metadata wildcard request
			crd, err = c.getForWildcardPartialMetadata(name)
		} else if clusterName != "*" {
			// Priority 4: normal CRD request
			crd, err = c.get(ctx, clusterName, name, identity)
		} else {
			return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
		}
	}

	if err != nil {
		return nil, err
	}

	if partialMetadataRequest {
		crd = shallowCopyCRDAndDeepCopyAnnotations(crd)
		addPartialMetadataCRDAnnotation(crd)

		if clusterName == "*" {
			crd.UID = types.UID(name + ".wildcard.partial-metadata")
		}
	}

	return crd, nil
}

// getForIdentityWildcard handles finding the right CRD for an incoming wildcard request with identity, such as
//
//	/clusters/*/apis/$group/$version/$resource:$identity.
func (c *crdLister) getForIdentityWildcard(ctx context.Context, name, identity string) (*apiextensionsv1.CustomResourceDefinition, error) {
	group, resource := crdNameToGroupResource(name)

	crs, err := c.listCachedResourcesByIdentityAndGR(identity, schema.GroupResource{
		Group:    group,
		Resource: resource,
	})
	if err != nil {
		return nil, err
	}
	if len(crs) == 0 {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	return c.synthesizeCRDForCachedResources(crs)
}

func (c *crdLister) getForWildcardPartialMetadata(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	group, resource := crdNameToGroupResource(name)
	crs, err := c.listCachedResourcesByGR(schema.GroupResource{Group: group, Resource: resource})
	if err != nil {
		return nil, err
	}
	if len(crs) > 0 {
		// Pick any one. Partial metadata requests don't care.
		return c.synthesizeCRDForCachedResources(crs[:1])
	}

	return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
}

func (c *crdLister) get(ctx context.Context, clusterName logicalcluster.Name, name, identity string) (*apiextensionsv1.CustomResourceDefinition, error) {
	group, resource := crdNameToGroupResource(name)
	gr := schema.GroupResource{Group: group, Resource: resource}

	// Find the identity hash from a CachedResource in the target cluster.
	crs, err := c.cachedResourcesLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, cr := range crs {
		matchingIdentity := identity == "" || cr.Status.IdentityHash == identity
		if matchingIdentity && cr.Spec.Group == group && cr.Spec.Resource == resource {
			matchingCRs, err := c.listCachedResourcesByIdentityAndGR(cr.Status.IdentityHash, gr)
			if err != nil {
				return nil, err
			}
			if len(matchingCRs) == 0 {
				return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
			}
			return c.synthesizeCRDForCachedResources(matchingCRs)
		}
	}

	return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
}

func crdNameToGroupResource(name string) (group, resource string) {
	parts := strings.SplitN(name, ".", 2)

	resource = parts[0]

	if len(parts) > 1 {
		group = parts[1]
	}

	if group == "core" {
		group = ""
	}

	return group, resource
}

func buildSchemalessCRDVersions(versions map[string]struct{}) []apiextensionsv1.CustomResourceDefinitionVersion {
	result := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(versions))
	for v := range versions {
		result = append(result, apiextensionsv1.CustomResourceDefinitionVersion{
			Name:   v,
			Served: true,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:                   "object",
					XPreserveUnknownFields: ptr.To(true),
				},
			},
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return k8sversion.CompareKubeAwareVersionStrings(result[i].Name, result[j].Name) < 0
	})
	// The last element after sorting is it the storage version.
	for i := range len(result) - 1 {
		result[i].Storage = false
	}
	result[len(result)-1].Storage = true
	return result
}

func syntheticCRDUID(identity string, gr schema.GroupResource, sortedVersions []string) types.UID {
	h := sha256.Sum256([]byte(identity + "|" + gr.String() + "|" + strings.Join(sortedVersions, ",")))
	return types.UID(strings.ToLower(base36.EncodeBytes(h[:])))
}

// synthesizeCRD builds a CRD from a set of CachedResources that share the same identity+GR.
func (c *crdClusterLister) synthesizeCRDForCachedResources(crs []*cachev1alpha1.CachedResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	gr := schema.GroupResource{
		Group:    crs[0].Spec.Group,
		Resource: crs[0].Spec.Resource,
	}
	kind := crs[0].Annotations[cachedresources.AnnotationResourceKind]
	scope := apiextensionsv1.ResourceScope(crs[0].Annotations[cachedresources.AnnotationResourceScope])
	identity := crs[0].Status.IdentityHash

	versionSet := make(map[string]struct{}, len(crs))
	for _, cr := range crs {
		gvr := schema.GroupVersionResource(cr.Spec.GroupVersionResource)
		versionSet[gvr.Version] = struct{}{}
	}
	if len(versionSet) == 0 {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), gr.String())
	}

	versions := buildSchemalessCRDVersions(versionSet)

	// Content-addressable UID: changes when the served version set changes,
	// forcing the apiextensions handler to rebuild serving info automatically.
	sortedVersionNames := make([]string, len(versions))
	for i, v := range versions {
		sortedVersionNames[i] = v.Name
	}
	uid := syntheticCRDUID(identity, gr, sortedVersionNames)

	names := apiextensionsv1.CustomResourceDefinitionNames{
		Plural:   gr.Resource,
		Singular: strings.ToLower(kind),
		Kind:     kind,
		ListKind: kind + "List",
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: string(uid),
			UID:  uid,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:          systemCachedCRDLogicalCluster.String(),
				apisv1alpha1.AnnotationAPIIdentityKey: identity,
				// We piggy-back on the same code paths that are used to handle bound CRDs,
				// i.e. support for identity storage prefix with RestOptionsGetter.
				"apis.kcp.io/bound-crd": "true",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    gr.Group,
			Names:    names,
			Scope:    scope,
			Versions: versions,
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			AcceptedNames: names,
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				// The apiextensions apiserver checks for these two conditions. They are applied automatically
				// for all apis.kcp.io/bound-crd CRDs, but since we are creating this CRD on the fly, we need
				// to apply these ourselves.
				{
					Type:   apiextensionsv1.Established,
					Status: apiextensionsv1.ConditionTrue,
				},
				{
					Type:   apiextensionsv1.NamesAccepted,
					Status: apiextensionsv1.ConditionTrue,
				},
			},
		},
	}

	return crd, nil
}
