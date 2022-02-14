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

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// inheritanceCRDLister is a CRD lister that add support for ClusterWorkspace API inheritance.
type inheritanceCRDLister struct {
	crdLister       apiextensionslisters.CustomResourceDefinitionLister
	workspaceLister tenancylisters.ClusterWorkspaceLister
}

var _ apiextensionslisters.CustomResourceDefinitionLister = (*inheritanceCRDLister)(nil)

// List lists all CustomResourceDefinitions in the underlying store matching selector. This method does not
// support scoping to logical clusters or workspace inheritance.
func (c *inheritanceCRDLister) List(selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.ListWithContext(context.Background(), selector)
}

// ListWithContext lists all CustomResourceDefinitions in the logical cluster associated with ctx that match
// selector. ClusterWorkspace API inheritance is also supported: if the ClusterWorkspace for ctx's logical cluster
// has spec.inheritFrom set, it will aggregate all CustomResourceDefinitions from the ClusterWorkspace named
// spec.inheritFrom with the CustomResourceDefinitions from the ClusterWorkspace for ctx's logical cluster.
func (c *inheritanceCRDLister) ListWithContext(ctx context.Context, selector labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}

	// Check for API inheritance
	inheriting := false
	inheritFrom := ""
	org, ws, err := helper.ParseLogicalClusterName(cluster.Name)
	if err != nil {
		return nil, err
	}

	// Chicken-and-egg: we need the ClusterWorkspace CRD to be created in the default admin logical cluster
	// before we can try to get said ClusterWorkspace, but if we fail listing because the ClusterWorkspace doesn't
	// exist, we'll never be able to create it. Only check if the target workspace exists for
	// non-default keys.
	if cluster.Name != helper.OrganizationCluster && c.workspaceLister != nil {
		targetWorkspaceKey := helper.WorkspaceKey(org, ws)
		workspace, err := c.workspaceLister.Get(targetWorkspaceKey)
		if err != nil && !apierrors.IsNotFound(err) {
			// Only return errors other than not-found. If we couldn't find the workspace, let's continue
			// to list the CRDs in ctx's logical cluster, at least until we have proper ClusterWorkspace permissions
			// requirements in place (i.e. reject all requests to a logical cluster if there isn't a
			// ClusterWorkspace for it). Otherwise, because this method is used for API discovery, you'd have
			// a weird situation where you could create a CRD but not be able to perform CRUD operations
			// on its CRs with kubectl (because it relies on discovery, and returning [] when we can't
			// find the ClusterWorkspace would mean CRDs from this logical cluster wouldn't be in discovery).
			return nil, err
		}

		if workspace != nil && workspace.Spec.InheritFrom != "" {
			if workspace.Spec.InheritFrom == helper.OrganizationCluster {
				// HACK: allow inheriting from the OrganizationCluster logical cluster
				inheriting = true
				inheritFrom = helper.OrganizationCluster
			} else {
				// Make sure the source workspace exists
				sourceWorkspaceKey := helper.WorkspaceKey(org, workspace.Spec.InheritFrom)
				_, err := c.workspaceLister.Get(sourceWorkspaceKey)
				switch {
				case err == nil:
					inheriting = true
					inheritFrom = helper.EncodeOrganizationAndWorkspace(org, workspace.Spec.InheritFrom)
				case apierrors.IsNotFound(err):
					// A NotFound error is ok. It means we can't inherit but we should still proceed below to list.
				default:
					// Only error if there was a problem checking for workspace existence
					return nil, err
				}
			}
		}
	}

	var ret []*apiextensionsv1.CustomResourceDefinition
	crds, err := c.crdLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for i := range crds {
		crd := crds[i]
		if crd.ClusterName == cluster.Name || (inheriting && crd.ClusterName == inheritFrom) {
			ret = append(ret, crd)
		}
	}

	return ret, nil
}

// Get gets a CustomResourceDefinitions in the underlying store by name. This method does not
// support scoping to logical clusters or workspace inheritance.
func (c *inheritanceCRDLister) Get(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.GetWithContext(context.Background(), name)
}

// GetWithContext gets a CustomResourceDefinitions in the logical cluster associated with ctx by
// name. ClusterWorkspace API inheritance is also supported: if ctx's logical cluster does not contain the
// CRD, and if the ClusterWorkspace for ctx's logical cluster has spec.inheritFrom set, it will try to find
// the CRD in the referenced ClusterWorkspace/logical cluster.
func (c *inheritanceCRDLister) GetWithContext(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}

	org, ws, err := helper.ParseLogicalClusterName(cluster.Name)
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
		// (across logical clusters) if the CRD of the related API Resource hasn't been added in the admin logical cluster first.
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
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: apiextensionsv1.SchemeGroupVersion.Group, Resource: "customresourcedefinitions"}, "")
		}

		return crd, nil
	}

	crdKey := clusters.ToClusterAwareKey(cluster.Name, name)
	crd, err = c.crdLister.Get(crdKey)
	if err != nil && !apierrors.IsNotFound(err) {
		// something went wrong w/the lister - could only happen if meta.Accessor() fails on an item in the store.
		return nil, err
	}

	// If we found the CRD in ctx's logical cluster, that takes priority.
	if crd != nil {
		return crd, nil
	}

	// ClusterWorkspace CRD is apparently not installed
	if c.workspaceLister == nil {
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	// Check for API inheritance
	targetWorkspaceKey := helper.WorkspaceKey(org, ws)
	workspace, err := c.workspaceLister.Get(targetWorkspaceKey)
	if err != nil {
		// If we're here it means ctx's logical cluster doesn't have the CRD and there isn't a
		// ClusterWorkspace for the logical cluster. Just return not-found.
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
		}

		return nil, err
	}

	if workspace.Spec.InheritFrom == "" {
		// If we're here it means ctx's logical cluster doesn't have the CRD, the ClusterWorkspace exists,
		// but it's not inheriting. Just return not-found.
		return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
	}

	var sourceWorkspaceCRDKey string
	if workspace.Spec.InheritFrom == helper.OrganizationCluster {
		// HACK: allow inheriting from the OrganizationCluster logical cluster
		sourceWorkspaceCRDKey = clusters.ToClusterAwareKey(helper.OrganizationCluster, name)
	} else {
		sourceWorkspaceKey := helper.WorkspaceKey(org, workspace.Spec.InheritFrom)
		if _, err := c.workspaceLister.Get(sourceWorkspaceKey); err != nil {
			// If we're here it means ctx's logical cluster doesn't have the CRD, the ClusterWorkspace exists,
			// we are inheriting, but the ClusterWorkspace we're inheriting from doesn't exist. Just return
			// not-found.
			if apierrors.IsNotFound(err) {
				return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
			}

			return nil, err
		}

		sourceWorkspaceCRDKey = clusters.ToClusterAwareKey(helper.EncodeOrganizationAndWorkspace(org, workspace.Spec.InheritFrom), name)
	}
	// Try to get the inherited CRD
	crd, err = c.crdLister.Get(sourceWorkspaceCRDKey)
	return crd, err
}

// findCRD tries to locate a CRD named crdName in crds. It returns the located CRD, if any, and a bool
// indicating that if there were multiple matches, they all have the same spec (true) or not (false).
func findCRD(crdName string, crds []*apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, bool) {
	var crd *apiextensionsv1.CustomResourceDefinition

	for _, aCRD := range crds {
		if aCRD.Name != crdName {
			continue
		}
		if crd == nil {
			crd = aCRD
		} else {
			if !equality.Semantic.DeepEqual(crd.Spec, aCRD.Spec) {
				//TODO(jmprusi): Review the logging level (https://github.com/kcp-dev/kcp/pull/328#discussion_r770683200)
				klog.Infof("Found multiple CRDs with the same name %q, but different specs: %v", crdName, cmp.Diff(crd.Spec, aCRD.Spec))
				return crd, false
			}
		}
	}

	return crd, true
}

// kcpAPIExtensionsSharedInformerFactory wraps the apiextensionsinformers.SharedInformerFactory so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsSharedInformerFactory struct {
	apiextensionsexternalversions.SharedInformerFactory
	workspaceLister tenancylisters.ClusterWorkspaceLister
}

// Apiextensions returns an apiextensions.Interface that supports inheritance when getting and
// listing CRDs.
func (f *kcpAPIExtensionsSharedInformerFactory) Apiextensions() apiextensions.Interface {
	i := f.SharedInformerFactory.Apiextensions()
	return &kcpAPIExtensionsApiextensions{
		Interface:       i,
		workspaceLister: f.workspaceLister,
	}
}

// kcpAPIExtensionsApiextensions wraps the apiextensions.Interface so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensions struct {
	apiextensions.Interface
	workspaceLister tenancylisters.ClusterWorkspaceLister
}

// V1 returns an apiextensionsinformerv1.Interface that supports inheritance when getting and
// listing CRDs.
func (i *kcpAPIExtensionsApiextensions) V1() apiextensionsinformerv1.Interface {
	v1i := i.Interface.V1()
	return &kcpAPIExtensionsApiextensionsV1{
		Interface:       v1i,
		workspaceLister: i.workspaceLister,
	}
}

// kcpAPIExtensionsApiextensionsV1 wraps the apiextensionsinformerv1.Interface so
// we can supply our own inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensionsV1 struct {
	apiextensionsinformerv1.Interface
	workspaceLister tenancylisters.ClusterWorkspaceLister
}

// CustomResourceDefinitions returns an apiextensionsinformerv1.CustomResourceDefinitionInformer
// that supports inheritance when getting and listing CRDs.
func (i *kcpAPIExtensionsApiextensionsV1) CustomResourceDefinitions() apiextensionsinformerv1.CustomResourceDefinitionInformer {
	c := i.Interface.CustomResourceDefinitions()
	return &kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer{
		CustomResourceDefinitionInformer: c,
		workspaceLister:                  i.workspaceLister,
	}
}

// kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer wraps the
// apiextensionsinformerv1.CustomResourceDefinitionInformer so we can supply our own
// inheritance-aware CRD lister.
type kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer struct {
	apiextensionsinformerv1.CustomResourceDefinitionInformer
	workspaceLister tenancylisters.ClusterWorkspaceLister
}

// Lister returns an apiextensionslisters.CustomResourceDefinitionLister
// that supports inheritance when getting and listing CRDs.
func (i *kcpAPIExtensionsApiextensionsV1CustomResourceDefinitionInformer) Lister() apiextensionslisters.CustomResourceDefinitionLister {
	originalLister := i.CustomResourceDefinitionInformer.Lister()
	l := &inheritanceCRDLister{
		crdLister:       originalLister,
		workspaceLister: i.workspaceLister,
	}
	return l
}
