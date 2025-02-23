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

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// byUID implements sort.Interface based on the UID field of CustomResourceDefinition.
type byUID []*apiextensionsv1.CustomResourceDefinition

func (u byUID) Len() int           { return len(u) }
func (u byUID) Less(i, j int) bool { return u[i].UID < u[j].UID }
func (u byUID) Swap(i, j int)      { u[i], u[j] = u[j], u[i] }

type conflictChecker struct {
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	listCRDs             func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error)

	clusterName  logicalcluster.Name
	crds         []*apiextensionsv1.CustomResourceDefinition
	crdToBinding map[string]*apisv1alpha1.APIBinding
}

// newConflictChecker creates a CRD conflict checker for the given cluster.
func newConflictChecker(clusterName logicalcluster.Name,
	listAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error),
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error),
	getCRD func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error),
	listCRDs func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error),
) (*conflictChecker, error) {
	ncc := &conflictChecker{
		listAPIBindings:      listAPIBindings,
		getAPIResourceSchema: getAPIResourceSchema,
		getCRD:               getCRD,
		listCRDs:             listCRDs,
		clusterName:          clusterName,
		crdToBinding:         map[string]*apisv1alpha1.APIBinding{},
	}

	// get bound CRDs
	bindings, err := ncc.listAPIBindings(ncc.clusterName)
	if err != nil {
		return nil, err
	}
	for _, b := range bindings {
		for _, br := range b.Status.BoundResources {
			crd, err := ncc.getCRD(SystemBoundCRDsClusterName, br.Schema.UID)
			if err != nil {
				return nil, err
			}

			ncc.crds = append(ncc.crds, crd)
			ncc.crdToBinding[crd.Name] = b
		}
	}

	// get normal CRDs
	crds, err := ncc.listCRDs(clusterName)
	if err != nil {
		return nil, err
	}
	ncc.crds = append(ncc.crds, crds...)

	sort.Sort(byUID(ncc.crds))

	return ncc, nil
}

// Check checks if the given schema from the given APIBinding conflicts with any
// CRD or any other APIBinding.
func (ncc *conflictChecker) Check(binding *apisv1alpha1.APIBinding, s *apisv1alpha1.APIResourceSchema) error {
	for _, crd := range ncc.crds {
		if other, found := ncc.crdToBinding[crd.Name]; found && other.Name == binding.Name {
			// don't check binding against itself
			continue
		}

		found, details := namesConflict(crd, s)
		if !found {
			// no conflict
			continue
		}

		if otherBinding, found := ncc.crdToBinding[crd.Name]; found {
			path := logicalcluster.NewPath(otherBinding.Spec.Reference.Export.Path)
			var boundTo string
			if path.Empty() {
				boundTo = fmt.Sprintf("local APIExport %q", otherBinding.Spec.Reference.Export.Name)
			} else {
				boundTo = fmt.Sprintf("APIExport %s", path.Join(otherBinding.Spec.Reference.Export.Name))
			}
			return fmt.Errorf("naming conflict with APIBinding %q bound to %s: %s", otherBinding.Name, boundTo, details)
		} else {
			return fmt.Errorf("naming conflict with CustomResourceDefinition %q: %s", crd.Name, details)
		}
	}

	return nil
}

func namesConflict(existing *apiextensionsv1.CustomResourceDefinition, incoming *apisv1alpha1.APIResourceSchema) (bool, string) {
	if existing.Spec.Group != incoming.Spec.Group {
		return false, ""
	}
	existingNames := sets.New[string]()
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

	existingKinds := sets.New[string]()
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
