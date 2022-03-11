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

	"github.com/google/go-cmp/cmp"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions"
	apiextensionsinformerv1 "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	apiextensionslisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// apiBindingAwareCRDLister is a CRD lister combines APIs coming from APIBindings with CRDs in a workspace.
type apiBindingAwareCRDLister struct {
	crdLister        apiextensionslisters.CustomResourceDefinitionLister
	workspaceLister  tenancylisters.ClusterWorkspaceLister
	apiBindingLister apislisters.APIBindingLister
}

var _ apiextensionslisters.CustomResourceDefinitionLister = (*apiBindingAwareCRDLister)(nil)

// List lists all CustomResourceDefinitions in the underlying store matching selector. This method does not
// support scoping to logical clusters or APIBindings.
func (c *apiBindingAwareCRDLister) List(selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.ListWithContext(context.Background(), selector)
}

// ListWithContext lists all CustomResourceDefinitions that come in via APIBindings as well as all in the current
// logical cluster.
func (c *apiBindingAwareCRDLister) ListWithContext(ctx context.Context, selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}

	var ret []*apiextensionsv1.CustomResourceDefinition
	crdName := func(crd *apiextensionsv1.CustomResourceDefinition) string {
		return crd.Spec.Names.Plural + "." + crd.Spec.Group
	}
	// crdName -> apiBinding
	seen := map[string]string{}

	apiBindings, err := c.apiBindingLister.ListWithContext(ctx, labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, apiBinding := range apiBindings {
		if apiBinding.ClusterName != cluster.Name {
			continue
		}
		if apiBinding.Status.Phase != apisv1alpha1.APIBindingPhaseBound {
			// TODO(ncdc): what if it's in the middle of rebinding and it takes some time - we won't include any
			// CRDs that were previously bound...
			continue
		}

		for _, boundResource := range apiBinding.Status.BoundResources {
			crdKey := clusters.ToClusterAwareKey("system:bound-crds", boundResource.Schema.UID)
			crd, err := c.crdLister.GetWithContext(ctx, crdKey)
			if err != nil {
				klog.Errorf("Error getting bound CRD %q: %v", crdKey, err)
				continue
			}
			ret = append(ret, crd)
			seen[crdName(crd)] = fmt.Sprintf("%s|%s", apiBinding.ClusterName, apiBinding.Name)
		}
	}

	crds, err := c.crdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for i := range crds {
		crd := crds[i]
		if crd.ClusterName != cluster.Name {
			continue
		}
		if apiBinding, exists := seen[crdName(crd)]; exists {
			klog.Infof("Skipping local CRD %s|%s because it came in via APIBinding %s", crd.ClusterName, crd.Name, apiBinding)
			continue
		}
		ret = append(ret, crd)
	}

	return ret, nil
}

// Get gets a CustomResourceDefinitions in the underlying store by name. This method does not
// support scoping to logical clusters or workspace inheritance.
func (c *apiBindingAwareCRDLister) Get(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.GetWithContext(context.Background(), name)
}

// GetWithContext gets a CustomResourceDefinitions in the logical cluster associated with ctx by
// name. ClusterWorkspace API inheritance is also supported: if ctx's logical cluster does not contain the
// CRD, and if the ClusterWorkspace for ctx's logical cluster has spec.inheritFrom set, it will try to find
// the CRD in the referenced ClusterWorkspace/logical cluster.
func (c *apiBindingAwareCRDLister) GetWithContext(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(name, ".") {
		name = name + "core"
	}

	var crd *apiextensionsv1.CustomResourceDefinition

	if cluster.Wildcard {
		// HACK: Search for the right logical cluster hosting the given CRD when watching or listing with wildcards.
		// This is a temporary fix for issue https://github.com/kcp-dev/kcp/issues/183: One cannot watch with wildcards
		// (across logical clusters) if the CRD of the related API Resource hasn't been added in the root logical cluster first.
		// The fix in this HACK is limited since the request will fail if 2 logical clusters contain CRDs for the same GVK
		// with non-equal specs (especially non-equal schemas).
		var crds []*apiextensionsv1.CustomResourceDefinition
		crds, err = c.crdLister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		var equal bool // true if all the found CRDs have the same spec
		crd, equal = findCRD(name, crds)
		if !equal {
			err = apierrors.NewInternalError(fmt.Errorf("error resolving resource: cannot watch across logical clusters for a resource type with several distinct schemas"))
			return nil, err
		}

		if crd == nil {
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: apiextensionsv1.SchemeGroupVersion.Group, Resource: "customresourcedefinitions"}, name)
		}

		return crd, nil
	}

	// Priority 1: see if it comes from any APIBindings
	parts := strings.SplitN(name, ".", 2)
	resource, group := parts[0], parts[1]

	// TODO(ncdc): APIResourceSchema requires that its group be "" for the core group,
	// See if we can unify things.
	if group == "core" {
		group = ""
	}

	apiBindings, err := c.apiBindingLister.ListWithContext(ctx, labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, apiBinding := range apiBindings {
		if apiBinding.ClusterName != cluster.Name {
			continue
		}
		if apiBinding.Status.Phase != apisv1alpha1.APIBindingPhaseBound {
			// TODO(ncdc): what if it's in the middle of rebinding and it takes some time - we won't include any
			// CRDs that were previously bound...
			continue
		}

		for _, boundResource := range apiBinding.Status.BoundResources {
			if boundResource.Group == group && boundResource.Resource == resource {
				crdKey := clusters.ToClusterAwareKey("system:bound-crds", boundResource.Schema.UID)
				crd, err = c.crdLister.Get(crdKey)
				if err != nil && !apierrors.IsNotFound(err) {
					// something went wrong w/the lister - could only happen if meta.Accessor() fails on an item in the store.
					return nil, err
				}

				if crd != nil {
					return crd, nil
				}

				// TODO(ncdc): if we got here, it means there is supposed to be a CRD coming from an APIBinding, but
				// the CRD doesn't exist for some reason. Does it make sense to return a 404 here, or keep going and
				// check other APIBindings and/or local CRDs?
				return nil, apierrors.NewNotFound(schema.GroupResource{Group: apiextensionsv1.SchemeGroupVersion.Group, Resource: "customresourcedefinitions"}, name)
			}
		}
	}

	// Priority 2: see if it exists in the current logical cluster
	crdKey := clusters.ToClusterAwareKey(cluster.Name, name)
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
func findCRD(crdName string, crds []*apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, bool) {
	var crd *apiextensionsv1.CustomResourceDefinition

	parts := strings.SplitN(crdName, ".", 2)
	resource := parts[0]
	group := parts[1]

	if group == "core" {
		group = ""
	}

	for _, aCRD := range crds {
		if aCRD.Spec.Group != group || aCRD.Spec.Names.Plural != resource {
			continue
		}

		if crd == nil {
			crd = aCRD
		} else {
			if !equality.Semantic.DeepEqual(crd.Spec, aCRD.Spec) {
				//TODO(jmprusi): Review the logging level (https://github.com/kcp-dev/kcp/pull/328#discussion_r770683200)
				klog.Infof("Found multiple CRDs with the same name group.resource %s.%s, but different specs: %v", group, resource, cmp.Diff(crd.Spec, aCRD.Spec))
				return nil, false
			}
		}
	}

	return crd, true
}

// kcpAPIExtensionsSharedInformerFactory wraps the apiextensionsinformers.SharedInformerFactory so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsSharedInformerFactory struct {
	apiextensionsexternalversions.SharedInformerFactory
	workspaceLister  tenancylisters.ClusterWorkspaceLister
	apiBindingLister apislisters.APIBindingLister
}

// Apiextensions returns an apiextensions.Interface that supports inheritance when getting and
// listing CRDs.
func (f *kcpAPIExtensionsSharedInformerFactory) Apiextensions() apiextensions.Interface {
	i := f.SharedInformerFactory.Apiextensions()
	return &kcpAPIExtensionsApiextensions{
		Interface:        i,
		workspaceLister:  f.workspaceLister,
		apiBindingLister: f.apiBindingLister,
	}
}

// kcpAPIExtensionsApiextensions wraps the apiextensions.Interface so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensions struct {
	apiextensions.Interface
	workspaceLister  tenancylisters.ClusterWorkspaceLister
	apiBindingLister apislisters.APIBindingLister
}

// V1 returns an apiextensionsinformerv1.Interface that supports inheritance when getting and
// listing CRDs.
func (i *kcpAPIExtensionsApiextensions) V1() apiextensionsinformerv1.Interface {
	v1i := i.Interface.V1()
	return &kcpAPIExtensionsApiextensionsV1{
		Interface:        v1i,
		workspaceLister:  i.workspaceLister,
		apiBindingLister: i.apiBindingLister,
	}
}

// kcpAPIExtensionsApiextensionsV1 wraps the apiextensionsinformerv1.Interface so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensionsV1 struct {
	apiextensionsinformerv1.Interface
	workspaceLister  tenancylisters.ClusterWorkspaceLister
	apiBindingLister apislisters.APIBindingLister
}

// CustomResourceDefinitions returns an apiextensionsinformerv1.CustomResourceDefinitionInformer
// that supports inheritance when getting and listing CRDs.
func (i *kcpAPIExtensionsApiextensionsV1) CustomResourceDefinitions() apiextensionsinformerv1.CustomResourceDefinitionInformer {
	c := i.Interface.CustomResourceDefinitions()
	return &kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer{
		CustomResourceDefinitionInformer: c,
		workspaceLister:                  i.workspaceLister,
		apiBindingLister:                 i.apiBindingLister,
	}
}

// kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer wraps the
// apiextensionsinformerv1.CustomResourceDefinitionInformer so we can supply our own
// inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer struct {
	apiextensionsinformerv1.CustomResourceDefinitionInformer
	workspaceLister  tenancylisters.ClusterWorkspaceLister
	apiBindingLister apislisters.APIBindingLister
}

// Lister returns an apiextensionslisters.CustomResourceDefinitionLister
// that supports inheritance when getting and listing CRDs.
func (i *kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer) Lister() apiextensionslisters.CustomResourceDefinitionLister {
	originalLister := i.CustomResourceDefinitionInformer.Lister()
	l := &apiBindingAwareCRDLister{
		crdLister:        originalLister,
		workspaceLister:  i.workspaceLister,
		apiBindingLister: i.apiBindingLister,
	}
	return l
}
