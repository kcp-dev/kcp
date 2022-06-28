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

package apibinding

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

type conflictChecker struct {
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)

	boundCRDs    []*apiextensionsv1.CustomResourceDefinition
	crdToBinding map[string]*apisv1alpha1.APIBinding
	crdIndexer   cache.Indexer
}

func (ncc *conflictChecker) getBoundCRDs(apiBindingToExclude *apisv1alpha1.APIBinding) error {
	clusterName := logicalcluster.From(apiBindingToExclude)

	apiBindings, err := ncc.listAPIBindings(clusterName)
	if err != nil {
		return err
	}

	ncc.crdToBinding = make(map[string]*apisv1alpha1.APIBinding)

	for _, apiBinding := range apiBindings {
		if apiBinding.Name == apiBindingToExclude.Name {
			continue
		}
		boundSchemaUIDs := sets.NewString()
		for _, boundResource := range apiBinding.Status.BoundResources {
			boundSchemaUIDs.Insert(boundResource.Schema.UID)
		}

		for _, resource := range apiBinding.Status.BoundResources {
			crd, err := ncc.getCRD(ShadowWorkspaceName, resource.Schema.UID)
			if err != nil {
				return err
			}

			ncc.boundCRDs = append(ncc.boundCRDs, crd)
			ncc.crdToBinding[crd.Name] = apiBinding
		}
	}

	return nil
}

func (ncc *conflictChecker) checkForConflicts(crd *apiextensionsv1.CustomResourceDefinition, apiBinding *apisv1alpha1.APIBinding) error {
	if err := ncc.getBoundCRDs(apiBinding); err != nil {
		return fmt.Errorf("error checking for naming conflicts for APIBinding %s|%s: error getting CRDs: %w", logicalcluster.From(apiBinding), apiBinding.Name, err)
	}

	for _, boundCRD := range ncc.boundCRDs {
		if namesConflict(boundCRD, crd) {
			conflict := ncc.crdToBinding[boundCRD.Name]
			return fmt.Errorf("naming conflict with APIBinding %s", conflict.Name)
		}
	}

	return ncc.gvrConflict(crd, apiBinding)
}

func (ncc *conflictChecker) gvrConflict(crd *apiextensionsv1.CustomResourceDefinition, apiBinding *apisv1alpha1.APIBinding) error {
	bindingClusterName := logicalcluster.From(apiBinding)
	rawBindingClusterCRDs, err := ncc.crdIndexer.ByIndex(indexByWorkspace, bindingClusterName.String())
	if err != nil {
		return err
	}
	for _, rawBindClusterCRD := range rawBindingClusterCRDs {
		bindingClusterCRD := rawBindClusterCRD.(*apiextensionsv1.CustomResourceDefinition)
		if bindingClusterCRD.Spec.Group == crd.Spec.Group && bindingClusterCRD.Spec.Names.Plural == crd.Spec.Names.Plural {
			return fmt.Errorf("cannot create %q CustomResourceDefinition with %q group and %q resource because it overlaps with %q CustomResourceDefinition in %q logical cluster",
				crd.Name, crd.Spec.Group, crd.Spec.Names.Plural, bindingClusterCRD.Name, bindingClusterName)
		}
	}
	return nil
}

func namesConflict(existing, incoming *apiextensionsv1.CustomResourceDefinition) bool {
	existingNames := sets.NewString()
	existingNames.Insert(existing.Status.AcceptedNames.Plural)
	existingNames.Insert(existing.Status.AcceptedNames.Singular)
	existingNames.Insert(existing.Status.AcceptedNames.ShortNames...)

	if existingNames.Has(incoming.Spec.Names.Plural) {
		return true
	}

	if existingNames.Has(incoming.Spec.Names.Singular) {
		return true
	}

	for _, shortName := range incoming.Spec.Names.ShortNames {
		if existingNames.Has(shortName) {
			return true
		}
	}

	existingKinds := sets.NewString()
	existingKinds.Insert(existing.Status.AcceptedNames.Kind)
	existingKinds.Insert(existing.Status.AcceptedNames.ListKind)

	if existingKinds.Has(incoming.Spec.Names.Kind) {
		return true
	}

	if existingKinds.Has(incoming.Spec.Names.ListKind) {
		return true
	}

	return false
}
