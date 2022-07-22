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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestNameConflictCheckerGetBoundCRDs(t *testing.T) {
	newAPIBinding := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("newBinding").
		WithWorkspaceReference("root:org:exportWS", "export0").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export0-schema1", "e0-s1").BoundAPIResource,
		).
		Build()

	existingBinding1 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing1").
		WithWorkspaceReference("root:org:exportWS", "export1").
		WithBoundResources(
			new(boundAPIResourceBuilder).WithSchema("export1-schema1", "e1-s1").BoundAPIResource,
			new(boundAPIResourceBuilder).WithSchema("export1-schema2", "e1-s2").BoundAPIResource,
		).
		Build()

	existingBinding2 := new(bindingBuilder).
		WithClusterName("root:org:ws").
		WithName("existing2").
		WithWorkspaceReference("root:org:exportWS", "export2").
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
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			return []*apisv1alpha1.APIBinding{
				newAPIBinding,
				existingBinding1,
				existingBinding2,
			}, nil
		},
		getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
			return apiExports[name], nil
		},
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemas[name], nil
		},
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
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

	newCRDFn := func(group, plural, singular, shortName, kind, listKind string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
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
		newCrd       *apiextensionsv1.CustomResourceDefinition
		wantConflict bool
	}{
		{
			name:         "same group, plural conflict",
			existingCrd:  existingCRDFn("acme.dev", "a", "", "", "", ""),
			newCrd:       newCRDFn("acme.dev", "a", "", "", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, singular conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "b", "", "", ""),
			newCrd:       newCRDFn("acme.dev", "", "b", "", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, shortnames conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "c", "", ""),
			newCrd:       newCRDFn("acme.dev", "", "", "c", "", ""),
			wantConflict: true,
		},

		{
			name:         "same group, kind conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "", "d", ""),
			newCrd:       newCRDFn("acme.dev", "", "", "", "d", ""),
			wantConflict: true,
		},

		{
			name:         "same group, listKind conflict",
			existingCrd:  existingCRDFn("acme.dev", "", "", "", "", "e"),
			newCrd:       newCRDFn("acme.dev", "", "", "", "", "e"),
			wantConflict: true,
		},

		{
			name:        "different group, no plural conflict",
			existingCrd: existingCRDFn("acme.dev", "a", "", "", "", ""),
			newCrd:      newCRDFn("new.acme.dev", "a", "", "", "", ""),
		},

		{
			name:        "different group, no singular conflict",
			existingCrd: existingCRDFn("acme.dev", "", "b", "", "", ""),
			newCrd:      newCRDFn("new.acme.dev", "", "b", "", "", ""),
		},

		{
			name:        "different group, no shortnames conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "c", "", ""),
			newCrd:      newCRDFn("new.acme.dev", "", "", "c", "", ""),
		},

		{
			name:        "different group, no kind conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "", "d", ""),
			newCrd:      newCRDFn("new.acme.dev", "", "", "", "d", ""),
		},

		{
			name:        "different group, no listKind conflict",
			existingCrd: existingCRDFn("acme.dev", "", "", "", "", "e"),
			newCrd:      newCRDFn("new.acme.dev", "", "", "", "", "e"),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			actualConflict, _ := namesConflict(scenario.existingCrd, scenario.newCrd)
			if actualConflict != scenario.wantConflict {
				t.Fatal("didn't expect to hit name conflict")
			}
		})
	}
}

func TestGVRConflict(t *testing.T) {
	scenarios := []struct {
		name        string
		initialCRDs []runtime.Object
		crd         *apiextensionsv1.CustomResourceDefinition
		binding     *apisv1alpha1.APIBinding
		wantErr     bool
	}{
		{
			name:    "no initial CRDs, no conflicts",
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			crd:     createCRD("", "crd1", "acmeGR", "acmeRS"),
		},
		{
			name: "no conflict when non-overlapping initial CRDs exist",
			initialCRDs: []runtime.Object{
				createCRD("root:example", "crd1", "acmeGR", "acmeRS"),
			},
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			crd:     createCRD("", "crd1", "acmeGR", "acmeRS"),
		},
		{
			name: "creating conflicting CRD fails",
			initialCRDs: []runtime.Object{
				createCRD("root:acme", "crd1", "acmeGR", "acmeRS"),
			},
			binding: new(bindingBuilder).WithClusterName("root:acme").WithName("newBinding").Build(),
			crd:     createCRD("", "crd1", "acmeGR", "acmeRS"),
			wantErr: true,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.AddIndexers(cache.Indexers{indexByWorkspace: indexByWorkspaceFunc}); err != nil {
				t.Fatal(err)
			}
			for _, obj := range scenario.initialCRDs {
				if err := indexer.Add(obj); err != nil {
					t.Error(err)
				}
			}
			c := &conflictChecker{crdIndexer: indexer}
			if err := c.gvrConflict(scenario.crd, scenario.binding); err != nil != scenario.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, scenario.wantErr)
			}
		})
	}
}

func createCRD(clusterName, name, group, resource string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, ClusterName: clusterName},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: resource},
		},
	}
}
