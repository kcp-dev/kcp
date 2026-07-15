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

package apibindingdeletion

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	testCluster      = "root:consumer"
	providerPath     = "root:provider"
	cowboySchemaName = "v1.cowboys.wildwest.dev"
	cowboySchemaUID  = "cowboy-schema-uid"
	identity         = "abc123"
)

func deletingBinding() *apisv1alpha2.APIBinding {
	now := metav1.Now()
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "wildwest",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
			Annotations:       map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
		Status: apisv1alpha2.APIBindingStatus{
			BoundResources: []apisv1alpha2.BoundAPIResource{
				{
					Group:           "wildwest.dev",
					Resource:        "cowboys",
					StorageVersions: []string{"v1alpha1"},
					Schema: apisv1alpha2.BoundAPIResourceSchema{
						Name:         cowboySchemaName,
						UID:          cowboySchemaUID,
						IdentityHash: identity,
					},
				},
			},
		},
	}
}

func candidateBinding(name, exportName string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: testCluster},
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath,
					Name: exportName,
				},
			},
		},
	}
}

func cowboysExport(name, identityHash, schemaName string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: providerPath},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: schemaName,
				},
			},
		},
		Status: apisv1alpha2.APIExportStatus{
			IdentityHash: identityHash,
		},
	}
}

func TestAdoptedResources(t *testing.T) {
	t.Parallel()

	cowboysGR := schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

	tests := []struct {
		name     string
		bindings []*apisv1alpha2.APIBinding
		exports  map[string]*apisv1alpha2.APIExport
		schemas  map[string]*apisv1alpha1.APIResourceSchema

		expectSuccessor string // "" means not adopted
	}{
		{
			name: "no candidates: not adopted",
		},
		{
			name:     "successor with same schema UID and identity: adopted",
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
			expectSuccessor: "cowboys",
		},
		{
			name:     "successor export has different identity: not adopted",
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", "other-identity", cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
		},
		{
			name:     "successor schema has different UID: not adopted",
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID("other-uid")}},
			},
		},
		{
			name: "successor being deleted itself: ignored",
			bindings: func() []*apisv1alpha2.APIBinding {
				b := candidateBinding("cowboys", "cowboys")
				now := metav1.Now()
				b.DeletionTimestamp = &now
				return []*apisv1alpha2.APIBinding{b}
			}(),
			exports: map[string]*apisv1alpha2.APIExport{
				"cowboys": cowboysExport("cowboys", identity, cowboySchemaName),
			},
			schemas: map[string]*apisv1alpha1.APIResourceSchema{
				cowboySchemaName: {ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}},
			},
		},
		{
			name:     "successor export unresolvable: not adopted",
			bindings: []*apisv1alpha2.APIBinding{candidateBinding("cowboys", "cowboys")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deleting := deletingBinding()
			c := &Controller{
				listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return append([]*apisv1alpha2.APIBinding{deleting}, tt.bindings...), nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					if export, ok := tt.exports[name]; ok {
						return export, nil
					}
					return nil, fmt.Errorf("APIExport %s|%s not found", path, name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if sch, ok := tt.schemas[name]; ok {
						return sch, nil
					}
					return nil, fmt.Errorf("APIResourceSchema %s|%s not found", cluster, name)
				},
			}

			adopted, err := c.adoptedResources(logicalcluster.Name(testCluster), deleting)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			successor, ok := adopted[cowboysGR]
			if (tt.expectSuccessor != "") != ok {
				t.Fatalf("expected adopted=%v, got %v (%v)", tt.expectSuccessor != "", ok, adopted)
			}
			if successor != tt.expectSuccessor {
				t.Errorf("expected successor %q, got %q", tt.expectSuccessor, successor)
			}
		})
	}
}

func TestDeleteAllCRsSkipsAdopted(t *testing.T) {
	t.Parallel()

	deleting := deletingBinding()

	deleted := map[schema.GroupVersionResource]bool{}
	c := &Controller{
		listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{deleting, candidateBinding("cowboys", "cowboys")}, nil
		},
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return cowboysExport("cowboys", identity, cowboySchemaName), nil
		},
		getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return &apisv1alpha1.APIResourceSchema{ObjectMeta: metav1.ObjectMeta{Name: cowboySchemaName, UID: types.UID(cowboySchemaUID)}}, nil
		},
		listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
			return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{
				*newPartialObject("wildwest.dev/v1alpha1", "Cowboy", "luke", "saloon", nil),
			}}, nil
		},
		deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
			deleted[gvr] = true
			return nil
		},
	}

	remaining, err := c.deleteAllCRs(context.Background(), deleting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions for adopted resources, got %v", deleted)
	}
	if len(remaining.gvrToNumRemaining) != 0 {
		t.Errorf("adopted resources must not count as remaining (would block finalization), got %v", remaining.gvrToNumRemaining)
	}
}
