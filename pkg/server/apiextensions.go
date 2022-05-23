/*
Copyright 2022 The KCP Authors.

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
	"fmt"
	"mime"
	_ "net/http/pprof"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionslisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// SystemCRDLogicalCluster is the logical cluster we install system CRDs into for now. These are needed
// to start wildcard informers until a "real" workspace gets them installed.
var SystemCRDLogicalCluster = logicalcluster.New("system:system-crds")

type systemCRDProvider struct {
	rootCRDs      sets.String
	orgCRDs       sets.String
	universalCRDs sets.String

	getClusterWorkspace func(key string) (*tenancyv1alpha1.ClusterWorkspace, error)
	getCRD              func(key string) (*apiextensionsv1.CustomResourceDefinition, error)
}

// NewSystemCRDProvider returns CRDs for certain cluster workspace types and the root workspace.
// TODO(sttts): This must be replaced by some non-hardcoded mechanism in the (near) future, probably by
//              using APIBindings. For now, this is our way to enforce to have no schema drift of these CRDs
//              as that would break wildcard informers.
func newSystemCRDProvider(
	getClusterWorkspace func(key string) (*tenancyv1alpha1.ClusterWorkspace, error),
	getCRD func(key string) (*apiextensionsv1.CustomResourceDefinition, error),
) *systemCRDProvider {
	p := &systemCRDProvider{
		rootCRDs: sets.NewString(
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "clusterworkspaces.tenancy.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "clusterworkspacetypes.tenancy.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "clusterworkspaceshards.tenancy.kcp.dev"),

			// the following is installed to get discovery and OpenAPI right. But it is actually
			// served by a native rest storage, projecting the clusterworkspaces.
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "workspaces.tenancy.kcp.dev"),
		),
		orgCRDs: sets.NewString(
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "clusterworkspaces.tenancy.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "clusterworkspacetypes.tenancy.kcp.dev"),

			// the following is installed to get discovery and OpenAPI right. But it is actually
			// served by a native rest storage, projecting the clusterworkspaces.
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "workspaces.tenancy.kcp.dev"),
		),
		universalCRDs: sets.NewString(
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "apiresourceimports.apiresource.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "negotiatedapiresources.apiresource.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "workloadclusters.workload.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "apiexports.apis.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "apibindings.apis.kcp.dev"),
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "apiresourceschemas.apis.kcp.dev"),
		),
		getClusterWorkspace: getClusterWorkspace,
		getCRD:              getCRD,
	}

	if utilfeature.DefaultFeatureGate.Enabled(kcpfeatures.LocationAPI) {
		p.rootCRDs.Insert(
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "locations.scheduling.kcp.dev"),
		)
		p.orgCRDs.Insert(
			clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "locations.scheduling.kcp.dev"),
		)

		// the following is installed to get discovery and OpenAPI right. But it is actually
		// served by a native rest storage, projecting the locations into this workspace.
		p.universalCRDs.Insert(clusters.ToClusterAwareKey(SystemCRDLogicalCluster, "locations.scheduling.kcp.dev"))
	}

	return p
}

func (p *systemCRDProvider) List(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var ret []*apiextensionsv1.CustomResourceDefinition

	for _, key := range p.Keys(clusterName).List() {
		crd, err := p.getCRD(key)
		if err != nil {
			klog.Errorf("Failed to get CRD %s for %s: %v", key, clusterName, err)
			// we shouldn't see this because getCRD is backed by a quorum-read client on cache-miss
			return nil, fmt.Errorf("error getting system CRD %q: %w", key, err)
		}

		ret = append(ret, crd)
	}

	return ret, nil
}

func (p *systemCRDProvider) Keys(clusterName logicalcluster.Name) sets.String {
	switch {
	case clusterName == tenancyv1alpha1.RootCluster:
		return p.rootCRDs
	case clusterName.HasPrefix(tenancyv1alpha1.RootCluster):
		parent, ws := clusterName.Split()

		workspaceKey := clusters.ToClusterAwareKey(parent, ws)
		clusterWorkspace, err := p.getClusterWorkspace(workspaceKey)
		if err != nil {
			// If a request for a system CRD comes in for a nonexistent workspace (either never existed, or was created
			// and then deleted, return no keys, which will result in a 404 being returned.

			if !apierrors.IsNotFound(err) {
				// Log any other errors (unexpected)
				klog.ErrorS(
					err,
					"Unable to determine system CRD keys: error getting clusterworkspace",
					"clusterName", clusterName.String(),
					"workspaceKey", workspaceKey,
				)
			}

			return sets.NewString()
		}

		switch clusterWorkspace.Spec.Type {
		case "Universal":
			return p.universalCRDs
		case "Organization", "Team":
			// TODO(sttts): this cannot be hardcoded. There might be other org-like types
			return p.orgCRDs
		}
	}

	return sets.NewString()
}

// apiBindingAwareCRDLister is a CRD lister combines APIs coming from APIBindings with CRDs in a workspace.
type apiBindingAwareCRDLister struct {
	kcpClusterClient     kcpclientset.ClusterInterface
	crdLister            apiextensionslisters.CustomResourceDefinitionLister
	workspaceLister      tenancylisters.ClusterWorkspaceLister
	apiBindingLister     apislisters.APIBindingLister
	apiBindingIndexer    cache.Indexer
	apiExportIndexer     cache.Indexer
	systemCRDProvider    *systemCRDProvider
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

var _ kcp.ClusterAwareCRDLister = &apiBindingAwareCRDLister{}

// List lists all CustomResourceDefinitions that come in via APIBindings as well as all in the current
// logical cluster retrieved from the context.
func (c *apiBindingAwareCRDLister) List(ctx context.Context, selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return nil, err
	}

	crdName := func(crd *apiextensionsv1.CustomResourceDefinition) string {
		return crd.Spec.Names.Plural + "." + crd.Spec.Group
	}

	selectAll := selector.Empty()
	matchesSelector := func(crd *apiextensionsv1.CustomResourceDefinition) bool {
		if selectAll {
			return true
		}

		return selector.Matches(labels.Set(crd.Labels))
	}

	// Seen keeps track of which CRDs have already been found from system and apibindings.
	seen := sets.NewString()

	kcpSystemCRDs, err := c.systemCRDProvider.List(clusterName)
	if err != nil {
		return nil, fmt.Errorf("error retrieving kcp system CRDs: %w", err)
	}

	// Priority 1: add system CRDs. These take priority over CRDs from APIBindings and CRDs from the local workspace.
	var ret = kcpSystemCRDs
	for i := range kcpSystemCRDs {
		seen.Insert(crdName(kcpSystemCRDs[i]))
	}

	apiBindings, err := c.apiBindingLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	// TODO(sttts): optimize this looping by using an informer index
	for _, apiBinding := range apiBindings {
		if logicalcluster.From(apiBinding) != clusterName {
			continue
		}
		if !conditions.IsTrue(apiBinding, apisv1alpha1.InitialBindingCompleted) {
			continue
		}

		for _, boundResource := range apiBinding.Status.BoundResources {
			crdKey := clusters.ToClusterAwareKey(apibinding.ShadowWorkspaceName, boundResource.Schema.UID)
			crd, err := c.crdLister.Get(crdKey)
			if err != nil {
				klog.Errorf("Error getting bound CRD %q: %v", crdKey, err)
				continue
			}

			if !matchesSelector(crd) {
				continue
			}

			// system CRDs take priority over APIBindings from the local workspace.
			if seen.Has(crdName(crd)) {
				// Came from system
				klog.Infof("Skipping APIBinding CRD %s|%s because it came in via system CRDs", crd.ClusterName, crd.Name)
				continue
			}

			// Priority 2: Add APIBinding CRDs. These take priority over those from the local workspace.

			// Add the APIExport identity hash as an annotation to the CRD so the RESTOptionsGetter can assign
			// the correct etcd resource prefix.
			crd = decorateCRDWithBinding(crd, boundResource.Schema.IdentityHash, apiBinding.DeletionTimestamp)

			ret = append(ret, crd)
			seen.Insert(crdName(crd))
		}
	}

	// TODO use scoping lister when available
	crds, err := c.crdLister.List(selector)
	if err != nil {
		return nil, err
	}
	for i := range crds {
		crd := crds[i]
		if logicalcluster.From(crd) != clusterName {
			continue
		}

		// system CRDs and local APIBindings take priority over CRDs from the local workspace.
		if seen.Has(crdName(crd)) {
			klog.Infof("Skipping local CRD %s|%s because it came in via APIBindings or system CRDs", crd.ClusterName, crd.Name)
			continue
		}

		// Priority 3: add local workspace CRDs that weren't already coming from APIBindings or kcp system.
		ret = append(ret, crd)
	}

	return ret, nil
}

func isPartialMetadataRequest(ctx context.Context) bool {
	if accept := ctx.Value(acceptHeaderContextKey).(string); len(accept) > 0 {
		if _, params, err := mime.ParseMediaType(accept); err == nil {
			return params["as"] == "PartialObjectMetadata" || params["as"] == "PartialObjectMetadataList"
		}
	}

	return false
}

func (c *apiBindingAwareCRDLister) Refresh(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	crdKey := clusters.ToClusterAwareKey(logicalcluster.From(crd), crd.Name)

	updatedCRD, err := c.crdLister.Get(crdKey)
	if err != nil {
		return nil, err
	}

	// Start with a shallow copy
	refreshed := shallowCopyCRD(updatedCRD)

	// If crd has the identity annotation, make sure it's added to refreshed
	if identity := crd.Annotations[apisv1alpha1.AnnotationAPIIdentityKey]; identity != "" {
		refreshed.Annotations[apisv1alpha1.AnnotationAPIIdentityKey] = identity
	}

	// If crd was only partial metadata, make sure refreshed is too
	if _, partialMetadata := crd.Annotations[annotationKeyPartialMetadata]; partialMetadata {
		partialMetadataCRD(refreshed)
	}

	return refreshed, nil
}

// Get gets a CustomResourceDefinition.
func (c *apiBindingAwareCRDLister) Get(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	var (
		crd *apiextensionsv1.CustomResourceDefinition
		err error
	)

	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return nil, err
	}

	// Priority 1: system CRD
	crd, err = c.getSystemCRD(clusterName, name)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	if crd == nil {
		// Not a system CRD

		if identity := IdentityFromContext(ctx); identity != "" {
			// Priority 2: identity request
			crd, err = c.getForIdentity(name, identity)
		} else if isPartialMetadataRequest(ctx) {
			// Priority 3: partial metadata
			crd, err = c.getForPartialMetadata(name)
		} else {
			// Priority 4: full data
			crd, err = c.getForFullData(clusterName, name)
		}
	}

	if err != nil {
		return nil, err
	}

	if isPartialMetadataRequest(ctx) {
		crd = shallowCopyCRD(crd)
		partialMetadataCRD(crd)
	}

	return crd, nil
}

// shallowCopyCRD makes a shallow copy of in, with a deep copy of in.ObjectMeta.Annotations.
func shallowCopyCRD(in *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	out := *in

	out.Annotations = make(map[string]string)

	for k, v := range in.Annotations {
		out.Annotations[k] = v
	}

	return &out
}

// decorateCRDWithBinding copy and mutate crd by
// 1. adding identity annotation
// 2. terminating status when apibinding is deleting
func decorateCRDWithBinding(in *apiextensionsv1.CustomResourceDefinition, identity string, deleteTime *metav1.Time) *apiextensionsv1.CustomResourceDefinition {
	out := shallowCopyCRD(in)

	out.Annotations[apisv1alpha1.AnnotationAPIIdentityKey] = identity

	if deleteTime.IsZero() {
		return out
	}

	out.Status.Conditions = make([]apiextensionsv1.CustomResourceDefinitionCondition, len(in.Status.Conditions))

	out.Status.Conditions = append(out.Status.Conditions, in.Status.Conditions...)

	out.DeletionTimestamp = deleteTime.DeepCopy()

	// This is not visible, only for apiextension to remove "create" verb when serving and discovery.
	apiextensionshelpers.SetCRDCondition(out, apiextensionsv1.CustomResourceDefinitionCondition{
		Type:   apiextensionsv1.Terminating,
		Status: apiextensionsv1.ConditionTrue,
	})

	return out
}

// partialMetadataCRD modifies CRD and replaces all version schemas with minimal ones suitable for partial object
// metadata.
func partialMetadataCRD(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.Annotations[annotationKeyPartialMetadata] = ""

	// set minimal schema that prunes everything but ObjectMeta
	for _, v := range crd.Spec.Versions {
		v.Schema = &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
			},
		}
	}
}

func (c *apiBindingAwareCRDLister) getForFullData(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	if clusterName == logicalcluster.Wildcard {
		return c.getWildcard(name)
	}

	return c.get(clusterName, name)
}

func (c *apiBindingAwareCRDLister) getWildcard(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	// TODO use an index by group+resource
	crds, err := c.crdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	crd, equivalentSchemas := findCRD(name, crds)
	if !equivalentSchemas {
		err = apierrors.NewInternalError(fmt.Errorf("error resolving resource: cannot watch across logical clusters for a resource type with several distinct schemas"))
		return nil, err
	}

	if crd == nil {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	return crd, nil
}

// getForIdentity handles finding the right CRD for an incoming wildcard request with identity, such as
// /clusters/*/apis/$group/$version/$resource:$identity.
func (c *apiBindingAwareCRDLister) getForIdentity(name, identity string) (*apiextensionsv1.CustomResourceDefinition, error) {
	group, resource := crdNameToGroupResource(name)

	indexKey := apibinding.IdentityGroupResourceKeyFunc(identity, group, resource)

	apiBindings, err := c.apiBindingIndexer.ByIndex(apibinding.IndexAPIBindingsByIdentityGroupResource, indexKey)
	if err != nil {
		return nil, err
	}

	if len(apiBindings) == 0 {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	// TODO(ncdc): if there are multiple bindings that match on identity/group/resource, do we need to consider some
	// sort of greatest-common-denominator for the CRD/schema?
	apiBinding := apiBindings[0].(*apisv1alpha1.APIBinding)

	var boundCRDName string

	for _, r := range apiBinding.Status.BoundResources {
		if r.Group == group && r.Resource == resource && r.Schema.IdentityHash == identity {
			boundCRDName = r.Schema.UID
			break
		}
	}

	if boundCRDName == "" {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	crdKey := clusters.ToClusterAwareKey(apibinding.ShadowWorkspaceName, boundCRDName)
	crd, err := c.crdLister.Get(crdKey)
	if err != nil {
		return nil, err
	}

	// Add the APIExport identity hash as an annotation to the CRD so the RESTOptionsGetter can assign
	// the correct etcd resource prefix. Use a shallow copy because deep copy is expensive (but deep copy the annotations).
	crd = decorateCRDWithBinding(crd, identity, apiBinding.DeletionTimestamp)

	return crd, nil
}

const annotationKeyPartialMetadata = "crd.kcp.dev/partial-metadata"

func (c *apiBindingAwareCRDLister) getForPartialMetadata(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	// TODO add index on CRDs by group+resource
	crds, err := c.crdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	group, resource := crdNameToGroupResource(name)

	crd := findFirstCRDMatchingGroupResource(group, resource, crds)
	if crd == nil {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	return crd, nil
}

func (c *apiBindingAwareCRDLister) getSystemCRD(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	if clusterName == logicalcluster.Wildcard {
		systemCRDKeyName := clusters.ToClusterAwareKey(SystemCRDLogicalCluster, name)
		return c.crdLister.Get(systemCRDKeyName)
	}

	systemCRDKeys := c.systemCRDProvider.Keys(clusterName)

	systemCRDKeyName := clusters.ToClusterAwareKey(SystemCRDLogicalCluster, name)

	if !systemCRDKeys.Has(systemCRDKeyName) {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	return c.crdLister.Get(systemCRDKeyName)
}

func (c *apiBindingAwareCRDLister) get(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	var crd *apiextensionsv1.CustomResourceDefinition

	// Priority 1: see if it comes from any APIBindings
	group, resource := crdNameToGroupResource(name)

	// TODO use scoped lister when ready
	apiBindings, err := c.apiBindingLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, apiBinding := range apiBindings {
		if logicalcluster.From(apiBinding) != clusterName {
			continue
		}
		if !conditions.IsTrue(apiBinding, apisv1alpha1.InitialBindingCompleted) {
			continue
		}

		for _, boundResource := range apiBinding.Status.BoundResources {
			if boundResource.Group == group && boundResource.Resource == resource {
				crdKey := clusters.ToClusterAwareKey(apibinding.ShadowWorkspaceName, boundResource.Schema.UID)
				crd, err = c.crdLister.Get(crdKey)
				if err != nil && apierrors.IsNotFound(err) {
					// If we got here, it means there is supposed to be a CRD coming from an APIBinding, but
					// the CRD doesn't exist for some reason.
					return nil, apierrors.NewServiceUnavailable(fmt.Sprintf("%s is currently unavailable", name))
				} else if err != nil {
					// something went wrong w/the lister - could only happen if meta.Accessor() fails on an item in the store.
					return nil, err
				}

				// Add the APIExport identity hash as an annotation to the CRD so the RESTOptionsGetter can assign
				// the correct etcd resource prefix.
				crd = decorateCRDWithBinding(crd, boundResource.Schema.IdentityHash, apiBinding.DeletionTimestamp)

				return crd, nil
			}
		}
	}

	// Priority 2: see if it exists in the current logical cluster
	crdKey := clusters.ToClusterAwareKey(clusterName, name)
	crd, err = c.crdLister.Get(crdKey)
	if err != nil && !apierrors.IsNotFound(err) {
		// something went wrong w/the lister - could only happen if meta.Accessor() fails on an item in the store.
		return nil, err
	}

	if crd != nil {
		return crd, nil
	}

	return nil, apierrors.NewNotFound(schema.GroupResource{Group: apiextensionsv1.SchemeGroupVersion.Group, Resource: "customresourcedefinitions"}, name)
}

// findCRD tries to locate a CRD named crdName in crds. It returns the located CRD, if any, and a bool
// indicating that if there were multiple matches, they all have the same spec (true) or not (false).
func findCRD(name string, crds []*apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, bool) {
	var crd *apiextensionsv1.CustomResourceDefinition

	group, resource := crdNameToGroupResource(name)

	for _, aCRD := range crds {
		if _, bound := aCRD.Annotations[apisv1alpha1.AnnotationBoundCRDKey]; bound {
			continue
		}
		if aCRD.Spec.Group != group || aCRD.Spec.Names.Plural != resource {
			continue
		}

		if crd == nil {
			crd = aCRD
		} else {
			if !equality.Semantic.DeepEqual(crd.Spec, aCRD.Spec) {
				return nil, false
			}
		}
	}

	return crd, true
}

func findFirstCRDMatchingGroupResource(group, resource string, crds []*apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	for _, crd := range crds {
		if _, bound := crd.Annotations[apisv1alpha1.AnnotationBoundCRDKey]; bound {
			continue
		}

		if crd.Spec.Group == group && crd.Spec.Names.Plural == resource {
			return crd
		}
	}

	return nil
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
