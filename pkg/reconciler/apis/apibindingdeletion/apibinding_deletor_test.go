/*
Copyright 2022 The kcp Authors.

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
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/reconciler/core/logicalclusterdeletion/deletion"
)

func TestMutateResourceRemainingStatus(t *testing.T) {
	t.Parallel()
	now := metav1.Now()
	apibinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
		},
		Status: apisv1alpha2.APIBindingStatus{},
	}

	tests := []struct {
		name                string
		resourceRemaining   gvrDeletionMetadataTotal
		expectErrorOnDelete error
		expectConditions    conditionsv1alpha1.Conditions
	}{
		{
			name: "resource is cleaned",
			resourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
				finalizersToNumRemaining: map[string]int{},
			},
			expectErrorOnDelete: nil,
			expectConditions: conditionsv1alpha1.Conditions{
				{
					Type:   apisv1alpha2.BindingResourceDeleteSuccess,
					Status: corev1.ConditionTrue,
				},
			},
		},
		{
			name: "some finalizer is remaining",
			resourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Version: "v1", Resource: "pods"}: 1,
				},
				finalizersToNumRemaining: map[string]int{
					"dev.kcp.io/test": 1,
				},
			},
			expectErrorOnDelete: &deletion.ResourcesRemainingError{Estimate: 5, Message: "finalizers dev.kcp.io/test in 1 resource instances remaining"},
			expectConditions: conditionsv1alpha1.Conditions{
				{
					Type:   apisv1alpha2.BindingResourceDeleteSuccess,
					Status: corev1.ConditionFalse,
					Reason: ResourceFinalizersRemainReason,
				},
			},
		},
		{
			name: "some resource is remaining",
			resourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Version: "v1", Resource: "pods"}: 1,
				},
				finalizersToNumRemaining: map[string]int{},
			},
			expectErrorOnDelete: &deletion.ResourcesRemainingError{Estimate: 5, Message: "resources pods. has 1 resource instances remaining"},
			expectConditions: conditionsv1alpha1.Conditions{
				{
					Type:   apisv1alpha2.BindingResourceDeleteSuccess,
					Status: corev1.ConditionFalse,
					Reason: ResourceRemainingReason,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller := &Controller{}
			apibindingCopy, err := controller.mutateResourceRemainingStatus(tt.resourceRemaining, apibinding.DeepCopy())

			if !matchErrors(err, tt.expectErrorOnDelete) {
				t.Errorf("expected error %q when syncing namespace, got %q", tt.expectErrorOnDelete, err)
			}
			for _, expCondition := range tt.expectConditions {
				cond := conditions.Get(apibindingCopy, expCondition.Type)
				if cond == nil {
					t.Fatalf("Missing status condition %v", expCondition.Type)
				}

				if cond.Status != expCondition.Status {
					t.Errorf("expect condition status %q, got %q for type %s", expCondition.Status, cond.Status, cond.Type)
				}

				if cond.Reason != expCondition.Reason {
					t.Errorf("expect condition reason %q, got %q for type %s", expCondition.Reason, cond.Reason, cond.Type)
				}
			}
		})
	}
}

func TestAPIBindingTerminating(t *testing.T) {
	t.Parallel()
	now := metav1.Now()
	apibinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
		},
		Status: apisv1alpha2.APIBindingStatus{
			BoundResources: []apisv1alpha2.BoundAPIResource{
				{
					Group:           "",
					Resource:        "pods",
					StorageVersions: []string{"v1"},
				},
				{
					Group:           "apps",
					Resource:        "deployments",
					StorageVersions: []string{"v1"},
				},
			},
		},
	}

	tests := []struct {
		name                      string
		existingObjects           map[schema.GroupVersionResource]*metav1.PartialObjectMetadata
		expectedResourceRemaining gvrDeletionMetadataTotal
	}{
		{
			name: "no resource left for apibinding to delete",
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
				finalizersToNumRemaining: map[string]int{},
				gvrsWaitingForSuccessor:  sets.New[schema.GroupVersionResource](),
			},
		},
		{
			name: "some resource remaining after apibinding deletion",
			existingObjects: map[schema.GroupVersionResource]*metav1.PartialObjectMetadata{
				corev1.SchemeGroupVersion.WithResource("pods"):        newPartialObject("v1", "Pod", "pod1", "ns1", nil),
				appsv1.SchemeGroupVersion.WithResource("deployments"): newPartialObject("apps/v1", "Deployment", "deploy1", "ns1", nil),
			},
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Group: "apps", Version: "v1", Resource: "deployments"}: 1,
					{Group: "", Version: "v1", Resource: "pods"}:            1,
				},
				finalizersToNumRemaining: map[string]int{},
				gvrsWaitingForSuccessor:  sets.New[schema.GroupVersionResource](),
			},
		},
		{
			name: "some resource remaining after apibinding deletion",
			existingObjects: map[schema.GroupVersionResource]*metav1.PartialObjectMetadata{
				corev1.SchemeGroupVersion.WithResource("pods"):        newPartialObject("v1", "Pod", "pod1", "ns1", []string{"test.kcp.io/finalizer"}),
				appsv1.SchemeGroupVersion.WithResource("deployments"): newPartialObject("apps/v1", "Deployment", "deploy1", "ns1", []string{"test.kcp.io/finalizer"}),
			},
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Group: "apps", Version: "v1", Resource: "deployments"}: 1,
					{Group: "", Version: "v1", Resource: "pods"}:            1,
				},
				finalizersToNumRemaining: map[string]int{
					"test.kcp.io/finalizer": 2,
				},
				gvrsWaitingForSuccessor: sets.New[schema.GroupVersionResource](),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller := &Controller{
				listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
				listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
					if tt.existingObjects == nil {
						return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{}}, nil
					}
					if item, recorded := tt.existingObjects[gvr]; recorded {
						return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{*item}}, nil
					}
					return &metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{}}, nil
				},
				deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
					return nil
				},
			}

			apibindingCopy := apibinding.DeepCopy()

			resourceRemaining, _ := controller.deleteAllCRs(context.TODO(), apibindingCopy)

			if !reflect.DeepEqual(resourceRemaining, tt.expectedResourceRemaining) {
				t.Errorf("expect remainint resource %v, got %v", tt.expectedResourceRemaining, resourceRemaining)
			}
		})
	}
}

func newPartialObject(apiversion, kind, name, namespace string, finlizers []string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiversion,
			Kind:       kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: finlizers,
		},
	}
}

// matchError returns true if errors match, false if they don't, compares by error message only for convenience which should be sufficient for these tests.
func matchErrors(e1, e2 error) bool {
	if e1 == nil && e2 == nil {
		return true
	}
	if e1 != nil && e2 != nil {
		return e1.Error() == e2.Error()
	}
	return false
}

func TestDeleteAllCRsUnservedStorageVersion(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: "example.dev", Resource: "widgets"}
	now := metav1.Now()
	apibinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
		},
		Status: apisv1alpha2.APIBindingStatus{
			APIExportClusterName: "provider",
			BoundResources: []apisv1alpha2.BoundAPIResource{{
				Group:           gr.Group,
				Resource:        gr.Resource,
				Schema:          apisv1alpha2.BoundAPIResourceSchema{Name: "v2.widgets.example.dev", UID: "uid-v2"},
				StorageVersions: []string{"v1alpha1", "v1alpha2"},
			}},
		},
	}
	boundSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: "v2.widgets.example.dev", UID: "uid-v2"},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Versions: []apisv1alpha1.APIResourceVersion{{Name: "v1alpha2", Served: true, Storage: true}},
		},
	}

	tests := []struct {
		name         string
		schemaErr    error
		served       sets.Set[string]
		expectListed []string
		expectErr    bool
	}{
		{
			name:         "unserved recorded storage version is not requested",
			served:       sets.New("v1alpha2"),
			expectListed: []string{"v1alpha2"},
		},
		{
			name:         "not found for the served version still fails",
			served:       sets.New[string](),
			expectListed: []string{"v1alpha2"},
			expectErr:    true,
		},
		{
			name:         "unresolvable schema falls back to recorded storage versions",
			schemaErr:    apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), boundSchema.Name),
			served:       sets.New("v1alpha2"),
			expectListed: []string{"v1alpha1", "v1alpha2"},
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// widget1 stands for an instance recorded under v1alpha1; the fake lister
			// already returns it through v1alpha2 (the apiserver converts it, which
			// requires the schema to still declare v1alpha1).
			stored := map[string]*metav1.PartialObjectMetadata{
				"widget1": newPartialObject("example.dev/v1alpha2", "Widget", "widget1", "ns1", nil),
			}
			var listed []string
			controller := &Controller{
				listAPIBindings: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if cluster != "provider" || name != boundSchema.Name {
						t.Errorf("unexpected schema lookup %s|%s", cluster, name)
					}
					return boundSchema, tt.schemaErr
				},
				listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
					listed = append(listed, gvr.Version)
					if !tt.served.Has(gvr.Version) {
						return nil, apierrors.NewNotFound(gr, "")
					}
					list := &metav1.PartialObjectMetadataList{}
					for _, obj := range stored {
						list.Items = append(list.Items, *obj)
					}
					return list, nil
				},
				deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
					for name, obj := range stored {
						if obj.Namespace == namespace {
							delete(stored, name)
						}
					}
					return nil
				},
			}

			remaining, err := controller.deleteAllCRs(context.TODO(), apibinding.DeepCopy())
			if !reflect.DeepEqual(listed, tt.expectListed) {
				t.Errorf("expected listed versions %v, got %v", tt.expectListed, listed)
			}
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := remaining.gvrToNumRemaining[gr.WithVersion("v1alpha2")]; got != 1 {
				t.Errorf("expected 1 remaining instance in the first pass, got %d (%v)", got, remaining.gvrToNumRemaining)
			}
			if len(stored) != 0 {
				t.Errorf("expected all instances to be deleted, %d left", len(stored))
			}

			remaining, err = controller.deleteAllCRs(context.TODO(), apibinding.DeepCopy())
			if err != nil {
				t.Fatalf("unexpected error in the second pass: %v", err)
			}
			if len(remaining.gvrToNumRemaining) != 0 || len(remaining.finalizersToNumRemaining) != 0 {
				t.Errorf("expected nothing remaining in the second pass, got %v", remaining)
			}
		})
	}
}

func TestDeletionVersions(t *testing.T) {
	t.Parallel()

	resource := apisv1alpha2.BoundAPIResource{
		Group:           "example.dev",
		Resource:        "widgets",
		Schema:          apisv1alpha2.BoundAPIResourceSchema{Name: "v2.widgets.example.dev", UID: "uid-v2"},
		StorageVersions: []string{"v1alpha1", "v1beta1"},
	}
	schemaWith := func(uid string, versions ...apisv1alpha1.APIResourceVersion) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{Name: resource.Schema.Name, UID: types.UID(uid)},
			Spec:       apisv1alpha1.APIResourceSchemaSpec{Versions: versions},
		}
	}

	tests := []struct {
		name          string
		exportCluster string
		schema        *apisv1alpha1.APIResourceSchema
		schemaErr     error
		expect        []string
	}{
		{
			name:          "served storage version",
			exportCluster: "provider",
			schema: schemaWith("uid-v2",
				apisv1alpha1.APIResourceVersion{Name: "v1alpha1", Served: true},
				apisv1alpha1.APIResourceVersion{Name: "v1beta1", Served: true, Storage: true},
			),
			expect: []string{"v1beta1"},
		},
		{
			name:          "unserved storage version uses first served version",
			exportCluster: "provider",
			schema: schemaWith("uid-v2",
				apisv1alpha1.APIResourceVersion{Name: "v1alpha1", Served: false},
				apisv1alpha1.APIResourceVersion{Name: "v1beta1", Served: true},
				apisv1alpha1.APIResourceVersion{Name: "v1", Served: false, Storage: true},
			),
			expect: []string{"v1beta1"},
		},
		{
			name:          "no served version falls back",
			exportCluster: "provider",
			schema:        schemaWith("uid-v2", apisv1alpha1.APIResourceVersion{Name: "v1beta1", Storage: true}),
			expect:        resource.StorageVersions,
		},
		{
			name:          "UID mismatch falls back",
			exportCluster: "provider",
			schema:        schemaWith("other", apisv1alpha1.APIResourceVersion{Name: "v1beta1", Served: true, Storage: true}),
			expect:        resource.StorageVersions,
		},
		{
			name:          "schema not found falls back",
			exportCluster: "provider",
			schemaErr:     apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), resource.Schema.Name),
			expect:        resource.StorageVersions,
		},
		{
			name:   "missing export cluster falls back",
			expect: resource.StorageVersions,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &Controller{
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if tt.schemaErr != nil {
						return nil, tt.schemaErr
					}
					if tt.schema == nil {
						t.Fatalf("unexpected schema lookup %s|%s", cluster, name)
					}
					return tt.schema, nil
				},
			}
			apibinding := &apisv1alpha2.APIBinding{Status: apisv1alpha2.APIBindingStatus{APIExportClusterName: tt.exportCluster}}
			if got := c.deletionVersions(klog.Background(), apibinding, resource); !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("expected %v, got %v", tt.expect, got)
			}
		})
	}
}
