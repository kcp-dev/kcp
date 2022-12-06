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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

func TestNameConflictCheckerGetBoundCRDs(t *testing.T) {
	newAPIBinding := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("newBinding").
		WithExportReference("root:org:exportWS", "export0").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export0-schema1", "e0-s1").BoundAPIResource,
		).
		Build()

	existingBinding1 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing1").
		WithExportReference("root:org:exportWS", "export1").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export1-schema1", "e1-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export1-schema2", "e1-s2").BoundAPIResource,
		).
		Build()

	existingBinding2 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing2").
		WithExportReference("root:org:exportWS", "export2").
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

	ncc := &conflictChecker{
		listAPIBindings: func(clusterName tenancy.Cluster) ([]*apisv1alpha1.APIBinding, error) {
			return []*apisv1alpha1.APIBinding{
				newAPIBinding,
				existingBinding1,
				existingBinding2,
			}, nil
		},
		getAPIExport: func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIExport, error) {
			return apiExports[name], nil
		},
		getAPIResourceSchema: func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemas[name], nil
		},
		getCRD: func(clusterName tenancy.Cluster, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
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
	existingCRDFn := func(group, plural, singular, shortName, kind, listKind string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: group},
			Status: apiextensionsv1.CustomResourceDefinitionStatus{AcceptedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     plural,
				Singular:   singular,
				ShortNames: []string{shortName},
				Kind:       kind,
				ListKind:   listKind,
			}},
		}
	}

	newSchemaFn := func(group, plural, singular, shortName, kind, listKind string) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: group,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:     plural,
					Singular:   singular,
					ShortNames: []string{shortName},
					Kind:       kind,
					ListKind:   listKind,
				},
			},
		}
	}

	scenarios := []struct {
		name         string
		existingCrd  *apiextensionsv1.CustomResourceDefinition
		schema       *apisv1alpha1.APIResourceSchema
		wantConflict bool
	}{
		{
			name:         "same group, plural conflict",
			existingCrd:  existingCRDFn("acme.dev", "a", "", "", "", ""),
			schema:       newSchemaFn("acme.dev", "a", "", "", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, singular conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "b", "", "", ""),
			schema:       newSchemaFn("acme.dev", "", "b", "", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, shortnames conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "c", "", ""),
			schema:       newSchemaFn("acme.dev", "", "", "c", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, kind conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "", "d", ""),
			schema:       newSchemaFn("acme.dev", "", "", "", "d", ""),
			wantConflict: true,
		},

		{
			name:         "same group, listKind conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "", "", "e"),
			schema:       newSchemaFn("acme.dev", "", "", "", "", "e"),
			wantConflict: true,
		},

		{
			name:        "different group, no plural conflict",
			existingCrd: existingCRDFn("acme.dev", "a", "", "", "", ""),
			schema:      newSchemaFn("new.acme.dev", "a", "", "", "", ""),
		},

		{
			name:        "different group, no singular conflict",
			existingCrd: existingCRDFn("acme.dev", "", "b", "", "", ""),
			schema:      newSchemaFn("new.acme.dev", "", "b", "", "", ""),
		},

		{
			name:        "different group, no shortnames conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "c", "", ""),
			schema:      newSchemaFn("new.acme.dev", "", "", "c", "", ""),
		},

		{
			name:        "different group, no kind conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "", "d", ""),
			schema:      newSchemaFn("new.acme.dev", "", "", "", "d", ""),
		},

		{
			name:        "different group, no listKind conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "", "", "e"),
			schema:      newSchemaFn("new.acme.dev", "", "", "", "", "e"),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			actualConflict, _ := namesConflict(scenario.existingCrd, scenario.schema)
			if actualConflict != scenario.wantConflict {
				t.Fatal("didn't expect to hit name conflict")
			}
		})
	}
}

func TestGVRConflict(t *testing.T) {
	scenarios := []struct {
		name        string
		initialCRDs []*apiextensionsv1.CustomResourceDefinition
		schema      *apisv1alpha1.APIResourceSchema
		binding     *apisv1alpha1.APIBinding
		wantErr     bool
	}{
		{
			name:    "no initial CRDs, no conflicts",
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			schema:  schemaFor(t, createCRD("", "crd1", "acmeGR", "acmeRS")),
		},
		{
			name: "no conflict when non-overlapping initial CRDs exist",
			initialCRDs: []*apiextensionsv1.CustomResourceDefinition{
				createCRD("root:example", "crd1", "acmeGR", "acmeRS"),
			},
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			schema:  schemaFor(t, createCRD("", "crd1", "acmeGR", "acmeRS")),
		},
		{
			name: "creating conflicting CRD fails",
			initialCRDs: []*apiextensionsv1.CustomResourceDefinition{
				createCRD("root:acme", "crd1", "acmeGR", "acmeRS"),
			},
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			schema:  schemaFor(t, createCRD("", "crd1", "acmeGR", "acmeRS")),
			wantErr: true,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			c := &conflictChecker{listCRDs: func(clusterName tenancy.Cluster) ([]*apiextensionsv1.CustomResourceDefinition, error) {
				var crds []*apiextensionsv1.CustomResourceDefinition
				for _, crd := range scenario.initialCRDs {
					if tenancy.From(crd) == clusterName {
						crds = append(crds, crd)
					}
				}
				return crds, nil
			}}
			if err := c.gvrConflict(scenario.schema, scenario.binding); err != nil != scenario.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, scenario.wantErr)
			}
		})
	}
}

func createCRD(clusterName, name, group, resource string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: resource},
		},
	}
}

func schemaFor(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) *apisv1alpha1.APIResourceSchema {
	t.Helper()

	s, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "someprefix")
	require.NoError(t, err)
	return s
}
