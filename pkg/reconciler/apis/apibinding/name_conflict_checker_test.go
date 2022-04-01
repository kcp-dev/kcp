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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestNameConflictCheckerGetBoundCRDs(t *testing.T) {
	newAPIBinding := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("newBinding").
		WithWorkspaceReference("exportWS", "export0").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export0-schema1", "e0-s1").BoundAPIResource,
		).
		Build()

	existingBinding1 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing1").
		WithWorkspaceReference("exportWS", "export1").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export1-schema1", "e1-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export1-schema2", "e1-s2").BoundAPIResource,
		).
		Build()

	existingBinding2 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing2").
		WithWorkspaceReference("exportWS", "export2").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export2-schema1", "e2-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export2-schema2", "e2-s2").BoundAPIResource,
		).
		Build()

	apiExports := map[string]*apisv1alpha1.APIExport{
		"export0": {
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{"export0-schema1"},
			},
		},
		"export1": {
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{"export1-schema1", "export1-schema2", "export1-schema3"},
			},
		},
		"export2": {
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{"export2-schema1", "export2-schema2", "export2-schema3"},
			},
		},
	}

	apiResourceSchemas := map[string]*apisv1alpha1.APIResourceSchema{
		"export0-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e0-s1"}},
		"export1-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s1"}},
		"export1-schema2": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s2"}},
		"export1-schema3": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s3"}},
		"export2-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s1"}},
		"export2-schema2": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s2"}},
		"export2-schema3": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s3"}},
	}

	ncc := &nameConflictChecker{
		listAPIBindings: func(clusterName logicalcluster.LogicalCluster) ([]*apisv1alpha1.APIBinding, error) {
			return []*apisv1alpha1.APIBinding{
				newAPIBinding,
				existingBinding1,
				existingBinding2,
			}, nil
		},
		getAPIExport: func(clusterName logicalcluster.LogicalCluster, name string) (*apisv1alpha1.APIExport, error) {
			return apiExports[name], nil
		},
		getAPIResourceSchema: func(clusterName logicalcluster.LogicalCluster, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemas[name], nil
		},
		getCRD: func(clusterName logicalcluster.LogicalCluster, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}, nil
		},
	}

	err := ncc.getBoundCRDs(newAPIBinding)
	require.NoError(t, err)

	expectedCRDs := sets.NewString("e1-s1", "e1-s2", "e2-s1", "e2-s2")
	actualCRDs := sets.NewString()
	for _, crd := range ncc.boundCRDs {
		actualCRDs.Insert(crd.Name)
	}
	require.True(t, expectedCRDs.Equal(actualCRDs), "bound CRDs mismatch: %s", cmp.Diff(expectedCRDs, actualCRDs))

	expectedMapping := map[string]*apisv1alpha1.APIBinding{
		"e1-s1": existingBinding1,
		"e1-s2": existingBinding1,
		"e2-s1": existingBinding2,
		"e2-s2": existingBinding2,
	}
	require.Equal(t, expectedMapping, ncc.crdToBinding)
}

func TestNamesConflict(t *testing.T) {
	existing := &apiextensionsv1.CustomResourceDefinition{
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			AcceptedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     "a",
				Singular:   "b",
				ShortNames: []string{"c", "d"},
				Kind:       "e",
				ListKind:   "f",
			},
		},
	}

	crdWithNames := func(names apiextensionsv1.CustomResourceDefinitionNames) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Names: names,
			},
		}
	}

	names := []string{"a", "b", "c", "d"}
	for _, v := range names {
		t.Run(fmt.Sprintf("plural-%s", v), func(t *testing.T) {
			require.True(t, namesConflict(existing, crdWithNames(apiextensionsv1.CustomResourceDefinitionNames{Plural: v})))
		})
		t.Run(fmt.Sprintf("singular-%s", v), func(t *testing.T) {
			require.True(t, namesConflict(existing, crdWithNames(apiextensionsv1.CustomResourceDefinitionNames{Singular: v})))
		})
		t.Run(fmt.Sprintf("shortnames-%s", v), func(t *testing.T) {
			require.True(t, namesConflict(existing, crdWithNames(apiextensionsv1.CustomResourceDefinitionNames{ShortNames: []string{v}})))
		})
	}

	kinds := []string{"e", "f"}
	for _, v := range kinds {
		t.Run(fmt.Sprintf("kind-%s", v), func(t *testing.T) {
			require.True(t, namesConflict(existing, crdWithNames(apiextensionsv1.CustomResourceDefinitionNames{Kind: v})))
		})
		t.Run(fmt.Sprintf("listkind-%s", v), func(t *testing.T) {
			require.True(t, namesConflict(existing, crdWithNames(apiextensionsv1.CustomResourceDefinitionNames{ListKind: v})))
		})
	}
}
