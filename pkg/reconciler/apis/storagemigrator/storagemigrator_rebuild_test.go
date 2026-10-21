/*
Copyright 2026 The kcp Authors.

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

package storagemigrator

import (
	"context"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestEnsureServingCRD(t *testing.T) {
	t.Parallel()

	const identity = "identity"
	schema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: "today.cowboys.wildwest.dev", UID: types.UID("schema-uid"), Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"}},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "wildwest.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "cowboys", Singular: "cowboy", Kind: "Cowboy", ListKind: "CowboyList"},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name: "v1alpha1", Served: true, Storage: true,
				Schema: runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
			}},
		},
	}
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys", Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"}},
		Spec: apisv1alpha2.APIExportSpec{Resources: []apisv1alpha2.ResourceSchema{{
			Name: "cowboys", Group: "wildwest.dev", Schema: schema.Name, Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
		}}},
		Status: apisv1alpha2.APIExportStatus{IdentityHash: identity},
	}
	br := func(key string, pending ...string) *apisv1alpha2.BoundAPIResource {
		return &apisv1alpha2.BoundAPIResource{
			Group:    "wildwest.dev",
			Resource: "cowboys",
			Schema:   apisv1alpha2.BoundAPIResourceSchema{Name: schema.Name, UID: string(schema.UID), IdentityHash: identity},
			Storage:  apisv1alpha2.BoundAPIResourceStorage{Key: key, PendingKeys: pending},
		}
	}
	established := func(name string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status:     apiextensionsv1.CustomResourceDefinitionStatus{Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}},
		}
	}

	scenarios := []struct {
		name        string
		br          *apisv1alpha2.BoundAPIResource
		existing    map[string]*apiextensionsv1.CustomResourceDefinition
		wantErr     string
		wantCreated string
	}{
		{
			name:     "the identity key is served by the schema UID CRD, present and established",
			br:       br(identity, "old"),
			existing: map[string]*apiextensionsv1.CustomResourceDefinition{"schema-uid": established("schema-uid")},
		},
		{
			name:        "another key gets a per-key CRD created when missing",
			br:          br("rekeyed", identity),
			existing:    map[string]*apiextensionsv1.CustomResourceDefinition{"schema-uid": established("schema-uid")},
			wantErr:     "to be established",
			wantCreated: indexers.BoundCRDNameForKey("schema-uid", identity, "rekeyed"),
		},
		{
			name: "a per-key CRD that is not established yet is waited for",
			br:   br("rekeyed", identity),
			existing: map[string]*apiextensionsv1.CustomResourceDefinition{
				indexers.BoundCRDNameForKey("schema-uid", identity, "rekeyed"): {ObjectMeta: metav1.ObjectMeta{Name: indexers.BoundCRDNameForKey("schema-uid", identity, "rekeyed")}},
			},
			wantErr: "to be established",
		},
		{
			name: "a per-key CRD that is established needs nothing",
			br:   br("rekeyed", identity),
			existing: map[string]*apiextensionsv1.CustomResourceDefinition{
				indexers.BoundCRDNameForKey("schema-uid", identity, "rekeyed"): established(indexers.BoundCRDNameForKey("schema-uid", identity, "rekeyed")),
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			created := ""
			c := &Controller{
				getBoundCRD: func(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					if crd, ok := scenario.existing[name]; ok {
						return crd, nil
					}
					return nil, apierrors.NewNotFound(apiextensionsv1.Resource("customresourcedefinitions"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if cluster == "provider" && name == schema.Name {
						return schema, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				createBoundCRD: func(_ context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
					created = crd.Name
					if crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey] != schema.Name {
						t.Errorf("expected the schema annotation on the created CRD, got %v", crd.Annotations)
					}
					return nil
				},
			}

			err := c.ensureServingCRD(context.Background(), scenario.br, export)
			switch {
			case scenario.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case scenario.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got none", scenario.wantErr)
			case scenario.wantErr != "" && !strings.Contains(err.Error(), scenario.wantErr):
				t.Fatalf("expected error containing %q, got %q", scenario.wantErr, err.Error())
			}
			if created != scenario.wantCreated {
				t.Errorf("expected created CRD %q, got %q", scenario.wantCreated, created)
			}
		})
	}
}
