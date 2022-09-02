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
	_ "net/http/pprof"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/munnerz/goautoneg"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionslisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

// SystemCRDLogicalCluster is the logical cluster we install system CRDs into for now. These are needed
// to start wildcard informers until a "real" workspace gets them installed.
var SystemCRDLogicalCluster = logicalcluster.New("system:system-crds")

// apiBindingAwareCRDLister is a CRD lister combines APIs coming from APIBindings with CRDs in a workspace.
type apiBindingAwareCRDLister struct {
	kcpClusterClient     kcpclient.ClusterInterface
	crdLister            apiextensionslisters.CustomResourceDefinitionLister
	crdIndexer           cache.Indexer
	workspaceLister      tenancylisters.ClusterWorkspaceLister
	apiBindingLister     apislisters.APIBindingLister
	apiBindingIndexer    cache.Indexer
	apiExportIndexer     cache.Indexer
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

var _ kcp.ClusterAwareCRDLister = &apiBindingAwareCRDLister{}

// List lists all CustomResourceDefinitions that come in via APIBindings as well as all in the current
// logical cluster retrieved from the context.
func (c *apiBindingAwareCRDLister) List(ctx context.Context, selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	logger := klog.FromContext(ctx)
	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return nil, err
	}
	logger = logger.WithValues("workspace", clusterName.String())

	crdName := func(crd *apiextensionsv1.CustomResourceDefinition) string {
		return crd.Spec.Names.Plural + "." + crd.Spec.Group
	}

	// Seen keeps track of which CRDs have already been found from system and apibindings.
	seen := sets.NewString()

	var ret []*apiextensionsv1.CustomResourceDefinition

	// Priority 1: add system CRDs. These take priority over CRDs from APIBindings and CRDs from the local workspace.
	systemCRDObjs, err := c.crdIndexer.ByIndex(byWorkspace, SystemCRDLogicalCluster.String())
	if err != nil {
		return nil, fmt.Errorf("error retrieving kcp system CRDs: %w", err)
	}
	for i := range systemCRDObjs {
		crd := systemCRDObjs[i].(*apiextensionsv1.CustomResourceDefinition)
		ret = append(ret, crd)
		seen.Insert(crdName(crd))
	}

	objs, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		apiBinding := obj.(*apisv1alpha1.APIBinding)
		if !conditions.IsTrue(apiBinding, apisv1alpha1.InitialBindingCompleted) {
			continue
		}

		for _, boundResource := range apiBinding.Status.BoundResources {
			crdKey := clusters.ToClusterAwareKey(apibinding.ShadowWorkspaceName, boundResource.Schema.UID)
			logger := logging.WithObject(logger, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:        boundResource.Schema.UID,
					Annotations: map[string]string{logicalcluster.AnnotationKey: apibinding.ShadowWorkspaceName.String()},
				},
			})
			crd, err := c.crdLister.Get(crdKey)
			if err != nil {
				logger.Error(err, "error getting bound CRD")
				continue
			}

			if !selector.Matches(labels.Set(crd.Labels)) {
				continue
			}

			// system CRDs take priority over APIBindings from the local workspace.
			if seen.Has(crdName(crd)) {
				// Came from system
				logger.Info("skipping APIBinding CRD because it came in via system CRDs")
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
	objs, err = c.crdIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		crd := obj.(*apiextensionsv1.CustomResourceDefinition)
		logger := logging.WithObject(logger, crd)

		if !selector.Matches(labels.Set(crd.Labels)) {
			continue
		}

		// system CRDs and local APIBindings take priority over CRDs from the local workspace.
		if seen.Has(crdName(crd)) {
			logger.Info("skipping local CRD because it came in via APIBindings or system CRDs")
			continue
		}

		// Priority 3: add local workspace CRDs that weren't already coming from APIBindings or kcp system.
		ret = append(ret, crd)
	}

	return ret, nil
}

func isPartialMetadataRequest(ctx context.Context) bool {
	accept := ctx.Value(acceptHeaderContextKey).(string)
	if accept == "" {
		return false
	}

	return isPartialMetadataHeader(accept)
}

func isPartialMetadataHeader(accept string) bool {
	clauses := goautoneg.ParseAccept(accept)
	for _, clause := range clauses {
		if clause.Params["as"] == "PartialObjectMetadata" || clause.Params["as"] == "PartialObjectMetadataList" {
			return true
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
	refreshed := shallowCopyCRDAndDeepCopyAnnotations(updatedCRD)

	// If crd has the identity annotation, make sure it's added to refreshed
	if identity := crd.Annotations[apisv1alpha1.AnnotationAPIIdentityKey]; identity != "" {
		refreshed.Annotations[apisv1alpha1.AnnotationAPIIdentityKey] = identity
	}

	// If crd was only partial metadata, make sure refreshed is too
	if _, partialMetadata := crd.Annotations[annotationKeyPartialMetadata]; partialMetadata {
		makePartialMetadataCRD(refreshed)

		if strings.HasSuffix(string(crd.UID), ".wildcard.partial-metadata") {
			refreshed.UID = crd.UID
		}
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

	partialMetadataRequest := isPartialMetadataRequest(ctx)

	if crd == nil {
		// Not a system CRD, so check in priority order: identity, wildcard, "normal" single cluster

		identity := IdentityFromContext(ctx)
		if clusterName == logicalcluster.Wildcard && identity != "" {
			// Priority 2: APIBinding CRD
			crd, err = c.getForIdentityWildcard(name, identity)
		} else if clusterName != logicalcluster.Wildcard && identity != "" {
			// identities are only supported on the wildcard cluster
			return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
		} else if clusterName == logicalcluster.Wildcard && partialMetadataRequest {
			// Priority 3: partial metadata wildcard request
			crd, err = c.getForWildcardPartialMetadata(name)
		} else if clusterName != logicalcluster.Wildcard {
			// Priority 4: normal CRD request
			crd, err = c.get(clusterName, name)
		} else {
			return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
		}
	}

	if err != nil {
		return nil, err
	}

	if partialMetadataRequest {
		crd = shallowCopyCRDAndDeepCopyAnnotations(crd)
		makePartialMetadataCRD(crd)

		if clusterName == logicalcluster.Wildcard {
			crd.UID = types.UID(name + ".wildcard.partial-metadata")
		}
	}

	return crd, nil
}

// shallowCopyCRDAndDeepCopyAnnotations makes a shallow copy of in, with a deep copy of in.ObjectMeta.Annotations.
func shallowCopyCRDAndDeepCopyAnnotations(in *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	out := *in

	out.Annotations = make(map[string]string, len(in.Annotations))
	for k, v := range in.Annotations {
		out.Annotations[k] = v
	}

	return &out
}

// decorateCRDWithBinding copy and mutate crd by
// 1. adding identity annotation
// 2. terminating status when apibinding is deleting
func decorateCRDWithBinding(in *apiextensionsv1.CustomResourceDefinition, identity string, deleteTime *metav1.Time) *apiextensionsv1.CustomResourceDefinition {
	out := shallowCopyCRDAndDeepCopyAnnotations(in)

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

// makePartialMetadataCRD modifies CRD and replaces all version schemas with minimal ones suitable for partial object
// metadata.
func makePartialMetadataCRD(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.Annotations[annotationKeyPartialMetadata] = ""

	// set minimal schema that prunes everything but ObjectMeta
	old := crd.Spec.Versions
	crd.Spec.Versions = make([]apiextensionsv1.CustomResourceDefinitionVersion, len(old))
	copy(crd.Spec.Versions, old)
	for i := range crd.Spec.Versions {
		crd.Spec.Versions[i].Schema = &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
			},
		}
	}
}

// getForIdentityWildcard handles finding the right CRD for an incoming wildcard request with identity, such as
//
//   /clusters/*/apis/$group/$version/$resource:$identity.
func (c *apiBindingAwareCRDLister) getForIdentityWildcard(name, identity string) (*apiextensionsv1.CustomResourceDefinition, error) {
	group, resource := crdNameToGroupResource(name)

	indexKey := identityGroupResourceKeyFunc(identity, group, resource)

	apiBindings, err := c.apiBindingIndexer.ByIndex(byIdentityGroupResource, indexKey)
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

func (c *apiBindingAwareCRDLister) getForWildcardPartialMetadata(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	objs, err := c.crdIndexer.ByIndex(byGroupResourceName, name)
	if err != nil {
		return nil, err
	}

	if len(objs) == 0 {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	return objs[0].(*apiextensionsv1.CustomResourceDefinition), nil
}

func (c *apiBindingAwareCRDLister) getSystemCRD(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	if clusterName == logicalcluster.Wildcard {
		systemCRDKeyName := clusters.ToClusterAwareKey(SystemCRDLogicalCluster, name)
		return c.crdLister.Get(systemCRDKeyName)
	}

	return c.crdLister.Get(clusters.ToClusterAwareKey(SystemCRDLogicalCluster, name))
}

func (c *apiBindingAwareCRDLister) get(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	var crd *apiextensionsv1.CustomResourceDefinition

	// Priority 1: see if it comes from any APIBindings
	group, resource := crdNameToGroupResource(name)

	objs, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		apiBinding := obj.(*apisv1alpha1.APIBinding)

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
