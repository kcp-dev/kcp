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
	"sort"

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// byUID implements sort.Interface based on the UID field of CustomResourceDefinition
type byUID []*apiextensionsv1.CustomResourceDefinition

func (u byUID) Len() int           { return len(u) }
func (u byUID) Less(i, j int) bool { return u[i].UID < u[j].UID }
func (u byUID) Swap(i, j int)      { u[i], u[j] = u[j], u[i] }

type conflictChecker struct {
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIExport         func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
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

		apiExportClusterName, err := getAPIExportClusterName(apiBinding)
		if err != nil {
			return err
		}

		apiExport, err := ncc.getAPIExport(apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName)
		if err != nil {
			return err
		}

		boundSchemaUIDs := sets.NewString()
		for _, boundResource := range apiBinding.Status.BoundResources {
			boundSchemaUIDs.Insert(boundResource.Schema.UID)
		}

		for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
			schema, err := ncc.getAPIResourceSchema(apiExportClusterName, schemaName)
			if err != nil {
				return err
			}

			if !boundSchemaUIDs.Has(string(schema.UID)) {
				continue
			}

			crd, err := ncc.getCRD(ShadowWorkspaceName, string(schema.UID))
			if err != nil {
				return err
			}

			ncc.boundCRDs = append(ncc.boundCRDs, crd)
			ncc.crdToBinding[crd.Name] = apiBinding
		}
	}

	sort.Sort(byUID(ncc.boundCRDs))
	return nil
}

func (ncc *conflictChecker) checkForConflicts(crd *apiextensionsv1.CustomResourceDefinition, apiBinding *apisv1alpha1.APIBinding) error {
	if err := ncc.getBoundCRDs(apiBinding); err != nil {
		return fmt.Errorf("error checking for naming conflicts for APIBinding %s|%s: error getting CRDs: %w", logicalcluster.From(apiBinding), apiBinding.Name, err)
	}

	for _, boundCRD := range ncc.boundCRDs {
		if foundConflict, details := namesConflict(boundCRD, crd); foundConflict {
			conflict := ncc.crdToBinding[boundCRD.Name]
			return fmt.Errorf("naming conflict with a bound API %s, %s", conflict.Name, details)
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

func namesConflict(existing, incoming *apiextensionsv1.CustomResourceDefinition) (bool, string) {
	if existing.Spec.Group != incoming.Spec.Group {
		return false, ""
	}
	existingNames := sets.NewString()
	existingNames.Insert(existing.Status.AcceptedNames.Plural)
	existingNames.Insert(existing.Status.AcceptedNames.Singular)
	existingNames.Insert(existing.Status.AcceptedNames.ShortNames...)

	if existingNames.Has(incoming.Spec.Names.Plural) {
		return true, fmt.Sprintf("spec.names.plural=%v is forbidden", incoming.Spec.Names.Plural)
	}

	if existingNames.Has(incoming.Spec.Names.Singular) {
		return true, fmt.Sprintf("spec.names.singular=%v is forbidden", incoming.Spec.Names.Singular)
	}

	for _, shortName := range incoming.Spec.Names.ShortNames {
		if existingNames.Has(shortName) {
			return true, fmt.Sprintf("spec.names.shortNames=%v is forbidden", incoming.Spec.Names.ShortNames)
		}
	}

	existingKinds := sets.NewString()
	existingKinds.Insert(existing.Status.AcceptedNames.Kind)
	existingKinds.Insert(existing.Status.AcceptedNames.ListKind)

	if existingKinds.Has(incoming.Spec.Names.Kind) {
		return true, fmt.Sprintf("spec.names.kind=%v is forbidden", incoming.Spec.Names.Kind)
	}

	if existingKinds.Has(incoming.Spec.Names.ListKind) {
		return true, fmt.Sprintf("spec.names.listKind=%v is forbidden", incoming.Spec.Names.ListKind)
	}

	return false, ""
}
