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
	"fmt"
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestSchedulingReconcile(t *testing.T) {
	testCases := []struct {
		name string

		placement   *schedulingv1alpha1.Placement
		location    *schedulingv1alpha1.Location
		syncTargets []*workloadv1alpha1.SyncTarget

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
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "/c1",
			},
		},
		{
			name:        "synctarget scheduled",
			placement:   newPlacement("test", "test-location", "c1"),
			location:    newLocation("test-location"),
			syncTargets: []*workloadv1alpha1.SyncTarget{newSyncTarget("c1", true)},
			expectedAnnotations: map[string]string{
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "/c1",
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
				workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: "/c2",
			},
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
			reconciler := &placementSchedulingReconciler{
				listSyncTarget: listSyncTarget,
				getLocation:    getLocation,
				patchPlacement: patchPlacement,
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
			workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey: fmt.Sprintf("/%s", synctarget),
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

func newSyncTarget(name string, ready bool) *workloadv1alpha1.SyncTarget {
	syncTarget := &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if ready {
		conditions.MarkTrue(syncTarget, conditionsapi.ReadyCondition)
	}

	return syncTarget
}
