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
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

func TestNameConflictCheckerGetBoundCRDs(t *testing.T) {
	newAPIBinding := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("newBinding").
		WithExportReference(logicalcluster.NewPath("root:org:exportWS"), "export0").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export0-schema1", "e0-s1").BoundAPIResource,
		).
		Build()

	existingBinding1 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing1").
		WithExportReference(logicalcluster.NewPath("root:org:exportWS"), "export1").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export1-schema1", "e1-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export1-schema2", "e1-s2").BoundAPIResource,
		).
		Build()

	existingBinding2 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing2").
		WithExportReference(logicalcluster.NewPath("root:org:exportWS"), "export2").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export2-schema1", "e2-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export2-schema2", "e2-s2").BoundAPIResource,
		).
		Build()

	apiResourceSchemas := map[string]*apisv1alpha1.APIResourceSchema{
		"export0-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e0-s1"}},
		"export1-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s1"}},
		"export1-schema2": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s2"}},
		"export1-schema3": {ObjectMeta: metav1.ObjectMeta{UID: "e1-s3"}},
		"export2-schema1": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s1"}},
		"export2-schema2": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s2"}},
		"export2-schema3": {ObjectMeta: metav1.ObjectMeta{UID: "e2-s3"}},
	}

	ncc, err := newConflictChecker("root:org:ws",
		func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{
				newAPIBinding,
				existingBinding1,
				existingBinding2,
			}, nil
		},
		func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemas[name], nil
		},
		func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return &apisv1alpha2.APIExport{}, nil
		},
		func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}, nil
		},
		func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			return nil, nil
		},
	)
	require.NoError(t, err)

	expectedCRDs := sets.New[string]("e0-s1", "e1-s1", "e1-s2", "e2-s1", "e2-s2")
	actualCRDs := sets.New[string]()
	for _, crd := range ncc.crds {
		actualCRDs.Insert(crd.Name)
	}
	require.True(t, expectedCRDs.Equal(actualCRDs), "bound CRDs mismatch: %s", cmp.Diff(expectedCRDs, actualCRDs))

	expectedMapping := map[string]*apisv1alpha2.APIBinding{
		"e0-s1": newAPIBinding,
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

func TestCRDs(t *testing.T) {
	scenarios := []struct {
		name        string
		initialCRDs []*apiextensionsv1.CustomResourceDefinition
		schema      *apisv1alpha1.APIResourceSchema
		binding     *apisv1alpha2.APIBinding
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
			c, err := newConflictChecker(logicalcluster.From(scenario.binding),
				func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
				func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, nil
				},
				func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{}, nil
				},
				func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return nil, nil
				},
				func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					var crds []*apiextensionsv1.CustomResourceDefinition
					for _, crd := range scenario.initialCRDs {
						if logicalcluster.From(crd) == clusterName {
							crds = append(crds, crd)
						}
					}
					return crds, nil
				},
			)
			require.NoError(t, err, "failed to create conflict checker")

			if err := c.Check(scenario.binding, scenario.schema); (err != nil) != scenario.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, scenario.wantErr)
			}
		})
	}
}

func getResource[T metav1.Object](name string, resources []T) (T, error) {
	for _, res := range resources {
		if res.GetName() == name {
			return res, nil
		}
	}
	var zero T
	return zero, fmt.Errorf("%s not found", name)
}

func TestVirtualResourceConflict(t *testing.T) {
	scenarios := map[string]struct {
		initialExport   *apisv1alpha2.APIExport
		initialBinding  *apisv1alpha2.APIBinding
		initialSchemas  []*apisv1alpha1.APIResourceSchema
		incomingExport  *apisv1alpha2.APIExport
		incomingBinding *apisv1alpha2.APIBinding
		incomingSchema  *apisv1alpha1.APIResourceSchema
		expectedErr     error
	}{
		"creating conflicting CRD fails": {
			initialExport: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-export",
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:  "wildwest.dev",
							Name:   "cowboys",
							Schema: "today.cowboys.wildwest.dev",
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{},
							},
						},
					},
				},
			},
			initialBinding: &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-binding",
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "my-export",
						},
					},
				},
				Status: apisv1alpha2.APIBindingStatus{
					BoundResources: []apisv1alpha2.BoundAPIResource{
						{
							Group:    "wildwest.dev",
							Resource: "cowboys",
							Schema: apisv1alpha2.BoundAPIResourceSchema{
								Name: "today.cowboys.wildwest.dev",
								UID:  "today.cowboys.wildwest.dev/schema-uid",
							},
						},
					},
				},
			},
			initialSchemas: []*apisv1alpha1.APIResourceSchema{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "today.cowboys.wildwest.dev",
						UID:  "today.cowboys.wildwest.dev/schema-uid",
					},
					Spec: apisv1alpha1.APIResourceSchemaSpec{
						Group: "wildwest.dev",
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Singular:   "cowboy",
							Plural:     "cowboys",
							Kind:       "Cowboy",
							ListKind:   "CowboyList",
							ShortNames: []string{"cow"},
						},
					},
				},
			},
			incomingExport: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-new-export",
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:  "wildwest.dev",
							Name:   "cowgirls",
							Schema: "today.cowgirls.wildwest.dev",
							Storage: apisv1alpha2.ResourceSchemaStorage{
								CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
							},
						},
					},
				},
			},
			incomingBinding: &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-new-binding",
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "my-new-export",
						},
					},
				},
				Status: apisv1alpha2.APIBindingStatus{
					BoundResources: []apisv1alpha2.BoundAPIResource{
						{
							Group:    "wildwest.dev",
							Resource: "thunders",
							Schema: apisv1alpha2.BoundAPIResourceSchema{
								Name: "today.cowgirls.wildwest.dev",
								UID:  "today.cowgirls.wildwest.dev/schema-uid",
							},
						},
					},
				},
			},
			incomingSchema: &apisv1alpha1.APIResourceSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name: "today.cowgirls.wildwest.dev",
					UID:  "today.cowgirls.wildwest.dev/schema-uid",
				},
				Spec: apisv1alpha1.APIResourceSchemaSpec{
					Group: "wildwest.dev",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Singular:   "cowgirl",
						Plural:     "cowgirls",
						Kind:       "Cowgirl",
						ListKind:   "CowgirlList",
						ShortNames: []string{"cow"},
					},
				},
			},
			expectedErr: fmt.Errorf(`naming conflict with APIBinding "my-binding" bound to local APIExport "my-export": spec.names.shortNames=[cw] is forbidden`),
		},
	}
	for name, s := range scenarios {
		t.Run(name, func(t *testing.T) {
			c, err := newConflictChecker(logicalcluster.From(s.incomingBinding),
				func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return []*apisv1alpha2.APIBinding{s.initialBinding}, nil
				},
				func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return getResource[*apisv1alpha1.APIResourceSchema](name, s.initialSchemas)
				},
				func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return getResource[*apisv1alpha2.APIExport](name, []*apisv1alpha2.APIExport{s.initialExport, s.incomingExport})
				},
				func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return getResource[*apiextensionsv1.CustomResourceDefinition](name, nil)
				},
				func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return nil, nil
				},
			)
			require.NoError(t, err, "failed to create conflict checker")

			err = c.Check(s.incomingBinding, s.incomingSchema)
			if s.expectedErr == nil {
				require.NoError(t, err, "conflictChecker.Check() failed unexpectedly")
			} else {
				require.EqualError(t, err, s.expectedErr.Error(), "conflictChecker.Check() returned an unexpected error")
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

func apiExportWithVirtualResource(grs []schema.GroupResource) *apisv1alpha2.APIExport {
	resources := make([]apisv1alpha2.ResourceSchema, 0, len(grs))
	for _, gr := range grs {
		resources = append(resources, apisv1alpha2.ResourceSchema{
			Group:  gr.Group,
			Name:   gr.Resource,
			Schema: fmt.Sprintf("someprefix.%s", gr.String()),
			Storage: apisv1alpha2.ResourceSchemaStorage{
				Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{},
			},
		})
	}
	return &apisv1alpha2.APIExport{
		Spec: apisv1alpha2.APIExportSpec{
			Resources: resources,
		},
	}
}

func schemaFor(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) *apisv1alpha1.APIResourceSchema {
	t.Helper()

	s, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "someprefix")
	require.NoError(t, err)
	return s
}
