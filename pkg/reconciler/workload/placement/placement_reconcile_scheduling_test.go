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

package placement

import (
	"context"
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestSchedulingReconcile(t *testing.T) {
	testCases := []struct {
		name string

		placement   *schedulingv1alpha1.Placement
		location    *schedulingv1alpha1.Location
		syncTargets []*workloadv1alpha1.SyncTarget
		apiBindings []*apisv1alpha1.APIBinding

		wantPatch           bool
		expectedAnnotations map[string]string
	}{
		{
			name:      "no location",
			placement: newPlacement("test", "test-location", ""),
		},
		{
			name:      "no synctarget",
			placement: newPlacement("test", "test-location", ""),
			location:  newLocation("test-location"),
		},
		{
			name:        "schedule one synctarget",
			placement:   newPlacement("test", "test-location", ""),
			location:    newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{newSyncTarget("c1", true)},
			wantPatch:   true,
			expectedAnnotations: map[string]string{
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa",
			},
		},
		{
			name:        "synctarget scheduled",
			placement:   newPlacement("test", "test-location", "c1"),
			location:    newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{newSyncTarget("c1", true)},
			expectedAnnotations: map[string]string{
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa",
			},
		},
		{
			name:                "unschedule synctarget",
			placement:           newPlacement("test", "test-location", "c1"),
			location:            newLocation("test-location"),
			syncTargets:         []*workloadv1alpha1.SyncTarget{newSyncTarget("c1", false)},
			wantPatch:           true,
			expectedAnnotations: map[string]string{},
		},
		{
			name:        "reschedule synctarget",
			placement:   newPlacement("test", "test-location", "c1"),
			location:    newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{newSyncTarget("c1", false), newSyncTarget("c2", true)},
			wantPatch:   true,
			expectedAnnotations: map[string]string{
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4",
			},
		},
		{
			name:      "schedule to syncTarget with compatible APIs",
			placement: newPlacement("test", "test-location", ""),
			location:  newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", true, workloadv1alpha1.ResourceToSync{GroupResource: apisv1alpha1.GroupResource{Resource: "services"}, State: workloadv1alpha1.ResourceSchemaIncompatibleState}),
				newSyncTarget("c2", true, workloadv1alpha1.ResourceToSync{GroupResource: apisv1alpha1.GroupResource{Resource: "services"}, State: workloadv1alpha1.ResourceSchemaAcceptedState}),
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				newAPIBinding("kubernetes", apisv1alpha1.BoundAPIResource{Resource: "services"}),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4",
			},
		},
		{
			name:      "no syncTarget has compatible APIs",
			placement: newPlacement("test", "test-location", ""),
			location:  newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", true, workloadv1alpha1.ResourceToSync{GroupResource: apisv1alpha1.GroupResource{Resource: "services"}, State: workloadv1alpha1.ResourceSchemaIncompatibleState}),
				newSyncTarget("c2", true),
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				newAPIBinding("kubernetes", apisv1alpha1.BoundAPIResource{Resource: "services"}),
			},
			wantPatch: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			listSyncTarget := func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
				return testCase.syncTargets, nil
			}
			getLocation := func(clusterName logicalcluster.Name, name string) (*schedulingv1alpha1.Location, error) {
				if testCase.location == nil {
					return nil, errors.NewNotFound(schema.GroupResource{}, name)
				}
				return testCase.location, nil
			}
			var patched bool
			patchPlacement := func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*schedulingv1alpha1.Placement, error) {
				patched = true
				nsData, _ := json.Marshal(testCase.placement)
				updatedData, err := jsonpatch.MergePatch(nsData, data)
				if err != nil {
					return nil, err
				}

				var patchedPlacement schedulingv1alpha1.Placement
				err = json.Unmarshal(updatedData, &patchedPlacement)
				if err != nil {
					return testCase.placement, err
				}
				return &patchedPlacement, err
			}
			listWorkloadAPIBindings := func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
				return testCase.apiBindings, nil
			}
			reconciler := &placementSchedulingReconciler{
				listSyncTarget:          listSyncTarget,
				getLocation:             getLocation,
				patchPlacement:          patchPlacement,
				listWorkloadAPIBindings: listWorkloadAPIBindings,
			}

			_, updated, err := reconciler.reconcile(context.TODO(), testCase.placement)
			require.NoError(t, err)
			require.Equal(t, testCase.wantPatch, patched)
			require.Equal(t, testCase.expectedAnnotations, updated.Annotations)
		})
	}
}

func newPlacement(name, location, synctarget string) *schedulingv1alpha1.Placement {
	placement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			NamespaceSelector: &metav1.LabelSelector{},
		},
		Status: schedulingv1alpha1.PlacementStatus{
			SelectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: location,
			},
		},
	}

	if len(synctarget) > 0 {
		placement.Annotations = map[string]string{
			workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: workloadv1alpha1.ToSyncTargetKey(logicalcluster.New(""), synctarget),
		}
	}

	return placement
}

func newLocation(name string) *schedulingv1alpha1.Location {
	return &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: schedulingv1alpha1.LocationSpec{
			InstanceSelector: &metav1.LabelSelector{},
		},
	}
}

func newSyncTarget(name string, ready bool, resources ...workloadv1alpha1.ResourceToSync) *workloadv1alpha1.SyncTarget {
	syncTarget := &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: workloadv1alpha1.SyncTargetStatus{
			SyncedResources: resources,
		},
	}

	if ready {
		conditions.MarkTrue(syncTarget, conditionsapi.ReadyCondition)
	}

	return syncTarget
}

func newAPIBinding(name string, resources ...apisv1alpha1.BoundAPIResource) *apisv1alpha1.APIBinding {
	return &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: apisv1alpha1.APIBindingStatus{
			BoundResources: resources,
		},
	}
}
