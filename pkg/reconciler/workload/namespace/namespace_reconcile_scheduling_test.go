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

	"github.com/kcp-dev/logicalcluster"
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
	now := time.Now().UTC().Format(time.RFC3339)
	testPlacement := newPlacement("test-placement", "test-location")
	testLocation := newLocation("test-location", map[string]string{})

	testCases := []struct {
		name string

		workloadClusters  []*workloadv1alpha1.WorkloadCluster
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
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "cluster1": string(workloadv1alpha1.ResourceStateSync),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			noPlacements: true,
			wantPatch:    true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                      "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "cluster1": now,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "cluster1": string(workloadv1alpha1.ResourceStateSync),
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
			name: "workloadclusters not found",
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
			name: "schedule a workloadcluster",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			placement: testPlacement,
			location:  testLocation,
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("test-cluster", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "no update when workloadclusters is scheduled",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("test-cluster", nil, corev1.ConditionTrue),
			},
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "workloadclusters becomes not ready",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("test-cluster", nil, corev1.ConditionFalse),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "select a new workloadclusters",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("test-cluster", nil, corev1.ConditionFalse),
				newWorkloadCluster("test-cluster-2", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster":   string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster-2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "scheduled cluster is removing",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now,
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: testPlacement,
			location:  testLocation,
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("test-cluster", nil, corev1.ConditionTrue),
				newWorkloadCluster("test-cluster-1", nil, corev1.ConditionTrue),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": now,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster":   string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster-1": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "remove clusters which is removing after grace period",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                          "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "test-cluster": time.Now().Add(-1 * (removingGracePeriod + 1)).UTC().Format(time.RFC3339),
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "test-cluster": string(workloadv1alpha1.ResourceStateSync),
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

			listWorkloadCluster := func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error) {
				if len(testCase.workloadClusters) == 0 {
					return []*workloadv1alpha1.WorkloadCluster{}, testCase.listWorkloadError
				}
				return testCase.workloadClusters, testCase.listWorkloadError
			}

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:       listPlacement,
				getLocation:         getLoaction,
				listWorkloadCluster: listWorkloadCluster,
				patchNamespace:      patchNamespaceFunc(&patched, ns),
				enqueueAfter:        func(*corev1.Namespace, time.Duration) {},
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
	now := time.Now().UTC().Format(time.RFC3339)

	testCases := []struct {
		name string

		workloadClusters []*workloadv1alpha1.WorkloadCluster
		locations        []*schedulingv1alpha1.Location
		placements       []*schedulingv1alpha1.Placement

		labels      map[string]string
		annotations map[string]string

		wantPatch           bool
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "schedule to two location",
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newWorkloadCluster("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
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
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "placement select the same location",
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
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
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are scheduled already",
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newWorkloadCluster("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
				newWorkloadCluster("c3", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newWorkloadCluster("c4", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
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
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are rescheduled when removing",
			workloadClusters: []*workloadv1alpha1.WorkloadCluster{
				newWorkloadCluster("c1", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newWorkloadCluster("c2", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
				newWorkloadCluster("c3", map[string]string{"loc": "loc1"}, corev1.ConditionTrue),
				newWorkloadCluster("c4", map[string]string{"loc": "loc2"}, corev1.ConditionTrue),
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
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c1": now,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c2": now,
			},
			labels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey:                                "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c1": now,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "c2": now,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c1": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c2": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c3": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.InternalClusterResourceStateLabelPrefix + "c4": string(workloadv1alpha1.ResourceStateSync),
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

			listWorkloadCluster := func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error) {
				return testCase.workloadClusters, nil
			}

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:       listPlacement,
				getLocation:         getLoaction,
				listWorkloadCluster: listWorkloadCluster,
				patchNamespace:      patchNamespaceFunc(&patched, ns),
				enqueueAfter:        func(*corev1.Namespace, time.Duration) {},
			}

			_, updated, err := reconciler.reconcile(context.TODO(), ns)
			require.NoError(t, err)
			require.Equal(t, testCase.wantPatch, patched)
			require.Equal(t, testCase.expectedAnnotations, updated.Annotations)
			require.Equal(t, testCase.expectedLabels, updated.Labels)
		})
	}
}

func newWorkloadCluster(name string, labels map[string]string, status corev1.ConditionStatus) *workloadv1alpha1.WorkloadCluster {
	return &workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: workloadv1alpha1.WorkloadClusterStatus{
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
