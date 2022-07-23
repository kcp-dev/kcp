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

package namespace

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestScheduling(t *testing.T) {
	now := time.Now()
	now3339 := now.UTC().Format(time.RFC3339)
	testPlacement := newPlacement("test-placement", "test-location")
	testLocation := newLocation("test-location", map[string]string{})

	testCases := []struct {
		name string

		syncTargets       []*workloadv1alpha1.SyncTarget
		listWorkloadError error
		location          *schedulingv1alpha1.Location
		getLocationError  error
		noPlacements      bool
		placement         *schedulingv1alpha1.Placement

		labels      map[string]string
		annotations map[string]string

		wantPatch           bool
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "placement not found",
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "cluster1": string(workloadv1alpha1.ResourceStateSync),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			noPlacements: true,
			wantPatch:    true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                      "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "cluster1": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "cluster1": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "location not found",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			placement: testPlacement,
			getLocationError: errors.NewNotFound(
				schema.GroupResource{Group: "scheudling.kcp.dev", Resource: "locations"},
				"test-location",
			),
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
		},
		{
			name: "synctargets not found",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			placement: testPlacement,
			location:  testLocation,
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
		},
		{
			name: "schedule a synctarget",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			placement: testPlacement,
			location:  testLocation,
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("test-cluster", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "no update when synctargets is scheduled",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("test-cluster", nil, corev1.ConditionTrue),
			},
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "synctargets becomes not ready",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("test-cluster", nil, corev1.ConditionFalse),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "select a new synctarget",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("test-cluster", nil, corev1.ConditionFalse),
				newSyncTarget("test-cluster-2", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster":   string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster-2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "scheduled cluster is removing",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now3339,
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("test-cluster", nil, corev1.ConditionTrue),
				newSyncTarget("test-cluster-1", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster":   string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster-1": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "remove clusters which is removing after grace period",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": time.Now().Add(-1 * (removingGracePeriod + 1)).UTC().Format(time.RFC3339),
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      testCase.labels,
					Annotations: testCase.annotations,
				},
			}

			listPlacement := func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Placement, error) {
				if testCase.noPlacements {
					return []*schedulingv1alpha1.Placement{}, nil
				}

				return []*schedulingv1alpha1.Placement{testCase.placement}, nil
			}

			getLoaction := func(clusterName logicalcluster.Name, name string) (*schedulingv1alpha1.Location, error) {
				if testCase.getLocationError != nil {
					return nil, testCase.getLocationError
				}

				return testCase.location, nil
			}

			listSyncTarget := func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
				if len(testCase.syncTargets) == 0 {
					return []*workloadv1alpha1.SyncTarget{}, testCase.listWorkloadError
				}
				return testCase.syncTargets, testCase.listWorkloadError
			}

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:  listPlacement,
				getLocation:    getLoaction,
				listSyncTarget: listSyncTarget,
				patchNamespace: patchNamespaceFunc(&patched, ns),
				enqueueAfter:   func(*corev1.Namespace, time.Duration) {},
				now:            func() time.Time { return now },
			}

			_, updated, err := reconciler.reconcile(context.TODO(), ns)
			require.NoError(t, err)
			require.Equal(t, testCase.wantPatch, patched)
			require.Equal(t, testCase.expectedAnnotations, updated.Annotations)
			require.Equal(t, testCase.expectedLabels, updated.Labels)
		})
	}
}

func TestMultiplePlacements(t *testing.T) {
	now := time.Now()
	now3339 := now.UTC().Format(time.RFC3339)

	testCases := []struct {
		name string

		syncTargets []*workloadv1alpha1.SyncTarget
		locations   []*schedulingv1alpha1.Location
		placements  []*schedulingv1alpha1.Placement

		labels      map[string]string
		annotations map[string]string

		wantPatch           bool
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "schedule to two location",
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newSyncTarget("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("loc1", map[string]string{"loc": "loc1"}),
				newLocation("loc2", map[string]string{"loc": "loc2"}),
			},
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1"),
				newPlacement("p2", "loc2"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "placement select the same location",
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("loc1", map[string]string{"loc": "loc1"}),
			},
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1"),
				newPlacement("p2", "loc1"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are scheduled already",
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newSyncTarget("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
				newSyncTarget("c3", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newSyncTarget("c4", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("loc1", map[string]string{"loc": "loc1"}),
				newLocation("loc2", map[string]string{"loc": "loc2"}),
			},
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1"),
				newPlacement("p2", "loc2"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are rescheduled when removing",
			syncTargets: []*workloadv1alpha1.SyncTarget{
				newSyncTarget("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newSyncTarget("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
				newSyncTarget("c3", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newSyncTarget("c4", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("loc1", map[string]string{"loc": "loc1"}),
				newLocation("loc2", map[string]string{"loc": "loc2"}),
			},
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1"),
				newPlacement("p2", "loc2"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c1": now3339,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c2": now3339,
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c1": now3339,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c2": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c3": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "c4": string(workloadv1alpha1.ResourceStateSync),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fmt.Printf("name %s\n", testCase.name)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      testCase.labels,
					Annotations: testCase.annotations,
				},
			}

			listPlacement := func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Placement, error) {
				return testCase.placements, nil
			}

			getLoaction := func(clusterName logicalcluster.Name, name string) (*schedulingv1alpha1.Location, error) {
				for _, loc := range testCase.locations {
					if loc.Name == name {
						return loc, nil
					}
				}
				return nil, errors.NewNotFound(
					schema.GroupResource{Group: "scheudling.kcp.dev", Resource: "locations"},
					name,
				)
			}

			listSyncTarget := func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
				return testCase.syncTargets, nil
			}

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:  listPlacement,
				getLocation:    getLoaction,
				listSyncTarget: listSyncTarget,
				patchNamespace: patchNamespaceFunc(&patched, ns),
				enqueueAfter:   func(*corev1.Namespace, time.Duration) {},
				now:            func() time.Time { return now },
			}

			_, updated, err := reconciler.reconcile(context.TODO(), ns)
			require.NoError(t, err)
			require.Equal(t, testCase.wantPatch, patched)
			require.Equal(t, testCase.expectedAnnotations, updated.Annotations)
			require.Equal(t, testCase.expectedLabels, updated.Labels)
		})
	}
}

func newSyncTarget(name string, labels map[string]string, status corev1.ConditionStatus) *workloadv1alpha1.SyncTarget {
	return &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: workloadv1alpha1.SyncTargetStatus{
			Conditions: []conditionsapi.Condition{
				{
					Type:   conditionsapi.ReadyCondition,
					Status: status,
				},
			},
		},
	}
}

func newLocation(name string, selector map[string]string) *schedulingv1alpha1.Location {
	return &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: schedulingv1alpha1.LocationSpec{
			InstanceSelector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
		},
	}
}

func newPlacement(name, location string) *schedulingv1alpha1.Placement {
	return &schedulingv1alpha1.Placement{
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
}
