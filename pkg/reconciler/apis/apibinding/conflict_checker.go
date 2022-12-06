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

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

// byUID implements sort.Interface based on the UID field of CustomResourceDefinition
type byUID []*apiextensionsv1.CustomResourceDefinition

func (u byUID) Len() int           { return len(u) }
func (u byUID) Less(i, j int) bool { return u[i].UID < u[j].UID }
func (u byUID) Swap(i, j int)      { u[i], u[j] = u[j], u[i] }

type conflictChecker struct {
	listAPIBindings      func(clusterName tenancy.Cluster) ([]*apisv1alpha1.APIBinding, error)
	getAPIExport         func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName tenancy.Cluster, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	listCRDs             func(clusterName tenancy.Cluster) ([]*apiextensionsv1.CustomResourceDefinition, error)

	boundCRDs    []*apiextensionsv1.CustomResourceDefinition
	crdToBinding map[string]*apisv1alpha1.APIBinding
}

func (ncc *conflictChecker) getBoundCRDs(apiBindingToExclude *apisv1alpha1.APIBinding) error {
	clusterName := tenancy.From(apiBindingToExclude)

	apiBindings, err := ncc.listAPIBindings(clusterName)
	if err != nil {
		return err
	}

	ncc.crdToBinding = make(map[string]*apisv1alpha1.APIBinding)

	for _, apiBinding := range apiBindings {
		if apiBinding.Name == apiBindingToExclude.Name {
			continue
		}

		if apiBinding.Spec.Reference.Export == nil {
			// this should not happen because of OpenAPI
			return fmt.Errorf("APIBinding %s|%s has no cluster reference", logicalcluster.From(apiBinding), apiBinding.Name)
		}
		apiExportClusterName := apiBinding.Spec.Reference.Export.Cluster

		apiExport, err := ncc.getAPIExport(apiExportClusterName, apiBinding.Spec.Reference.Export.Name)
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

func (ncc *conflictChecker) checkForConflicts(schema *apisv1alpha1.APIResourceSchema, apiBinding *apisv1alpha1.APIBinding) error {
	if err := ncc.getBoundCRDs(apiBinding); err != nil {
		return fmt.Errorf("error checking for naming conflicts for APIBinding %s|%s: error getting CRDs: %w", logicalcluster.From(apiBinding), apiBinding.Name, err)
	}

	for _, boundCRD := range ncc.boundCRDs {
		if foundConflict, details := namesConflict(boundCRD, schema); foundConflict {
			conflict := ncc.crdToBinding[boundCRD.Name]
			return fmt.Errorf("naming conflict with a bound API %s, %s", conflict.Name, details)
		}
	}

	return ncc.gvrConflict(schema, apiBinding)
}

func (ncc *conflictChecker) gvrConflict(schema *apisv1alpha1.APIResourceSchema, apiBinding *apisv1alpha1.APIBinding) error {
	bindingClusterName := tenancy.From(apiBinding)
	bindingClusterCRDs, err := ncc.listCRDs(bindingClusterName)
	if err != nil {
		return err
	}
	for _, bindingClusterCRD := range bindingClusterCRDs {
		if bindingClusterCRD.Spec.Group == schema.Spec.Group && bindingClusterCRD.Spec.Names.Plural == schema.Spec.Names.Plural {
			return fmt.Errorf("cannot create CustomResourceDefinition with %q group and %q resource because it overlaps with %q CustomResourceDefinition in %q logical cluster",
				schema.Spec.Group, schema.Spec.Names.Plural, bindingClusterCRD.Name, bindingClusterName)
		}
	}
	return nil
}

func namesConflict(existing *apiextensionsv1.CustomResourceDefinition, incoming *apisv1alpha1.APIResourceSchema) (bool, string) {
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
