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

package apibindingdeletion

import (
	"context"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion/deletion"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	utilruntime.Must(metav1.AddMetaToScheme(scheme))
}

func TestMutateResourceRemainingStatus(t *testing.T) {
	now := metav1.Now()
	apibinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
		},
		Status: apisv1alpha1.APIBindingStatus{},
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
					Type:   apisv1alpha1.BindingResourceDeleteSuccess,
					Status: v1.ConditionTrue,
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
			expectErrorOnDelete: &deletion.ResourcesRemainingError{Estimate: 5},
			expectConditions: conditionsv1alpha1.Conditions{
				{
					Type:   apisv1alpha1.BindingResourceDeleteSuccess,
					Status: v1.ConditionFalse,
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
			expectErrorOnDelete: &deletion.ResourcesRemainingError{Estimate: 5},
			expectConditions: conditionsv1alpha1.Conditions{
				{
					Type:   apisv1alpha1.BindingResourceDeleteSuccess,
					Status: v1.ConditionFalse,
					Reason: ResourceRemainingReason,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
	now := metav1.Now()
	apibinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			DeletionTimestamp: &now,
			Finalizers:        []string{APIBindingFinalizer},
		},
		Status: apisv1alpha1.APIBindingStatus{
			BoundResources: []apisv1alpha1.BoundAPIResource{
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
		existingObject            []runtime.Object
		metadataClientActionSet   metaActionSet
		expectErrorOnDelete       error
		expectedResourceRemaining gvrDeletionMetadataTotal
	}{
		{
			name:           "no resource left for apibinding to delete",
			existingObject: []runtime.Object{},
			metadataClientActionSet: []metaAction{
				{"pods", "list"},
				{"deployments", "list"},
			},
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
				finalizersToNumRemaining: map[string]int{},
			},
		},
		{
			name: "some resource remaining after apibinding deletion",
			existingObject: []runtime.Object{
				newPartialObject("v1", "Pod", "pod1", "ns1", nil),
				newPartialObject("apps/v1", "Deployment", "deploy1", "ns1", nil),
			},
			metadataClientActionSet: []metaAction{
				{"pods", "list"},
				{"pods", "delete-collection"},
				{"deployments", "list"},
				{"deployments", "delete-collection"},
			},
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Group: "apps", Version: "v1", Resource: "deployments"}: 1,
					{Group: "", Version: "v1", Resource: "pods"}:            1,
				},
				finalizersToNumRemaining: map[string]int{},
			},
		},
		{
			name: "some resource remaining after apibinding deletion",
			existingObject: []runtime.Object{
				newPartialObject("v1", "Pod", "pod1", "ns1", []string{"test.kcp.io/finalizer"}),
				newPartialObject("apps/v1", "Deployment", "deploy1", "ns1", []string{"test.kcp.io/finalizer"}),
			},
			metadataClientActionSet: []metaAction{
				{"pods", "list"},
				{"pods", "delete-collection"},
				{"deployments", "list"},
				{"deployments", "delete-collection"},
			},
			expectedResourceRemaining: gvrDeletionMetadataTotal{
				gvrToNumRemaining: map[schema.GroupVersionResource]int{
					{Group: "apps", Version: "v1", Resource: "deployments"}: 1,
					{Group: "", Version: "v1", Resource: "pods"}:            1,
				},
				finalizersToNumRemaining: map[string]int{
					"test.kcp.io/finalizer": 2,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockMetadataClient := metadatafake.NewSimpleMetadataClient(scheme, tt.existingObject...)
			controller := &Controller{
				metadataClient: mockMetadataClient,
			}

			apibindingCopy := apibinding.DeepCopy()

			resourceRemaining, _ := controller.deleteAllCRs(context.TODO(), apibindingCopy)

			if !reflect.DeepEqual(resourceRemaining, tt.expectedResourceRemaining) {
				t.Errorf("expect remainint resource %v, got %v", tt.expectedResourceRemaining, resourceRemaining)
			}

			if len(mockMetadataClient.Actions()) != len(tt.metadataClientActionSet) {
				t.Fatalf("mismatched actions, expect %d actions, got %d actions", len(tt.metadataClientActionSet), len(mockMetadataClient.Actions()))
			}

			for index, action := range mockMetadataClient.Actions() {
				if !tt.metadataClientActionSet.match(action) {
					t.Errorf("expect action for resource %q for verb %q but got %v", tt.metadataClientActionSet[index].resource, tt.metadataClientActionSet[index].verb, action)
				}
			}
		})
	}
}

type metaAction struct {
	resource string
	verb     string
}

type metaActionSet []metaAction

func (m metaActionSet) match(action clienttesting.Action) bool {
	for _, a := range m {
		if action.Matches(a.verb, a.resource) {
			return true
		}
	}

	return false
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

// matchError returns true if errors match, false if they don't, compares by error message only for convenience which should be sufficient for these tests
func matchErrors(e1, e2 error) bool {
	if e1 == nil && e2 == nil {
		return true
	}
	if e1 != nil && e2 != nil {
		return e1.Error() == e2.Error()
	}
	return false
}
