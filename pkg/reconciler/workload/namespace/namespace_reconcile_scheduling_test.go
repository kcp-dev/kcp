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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestScheduling(t *testing.T) {
	now := time.Now()
	now3339 := now.UTC().Format(time.RFC3339)

	testCases := []struct {
		name string

		noPlacements bool
		placement    *schedulingv1alpha1.Placement

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
			name: "schedule a synctarget",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			placement: newPlacement("test-placement", "test-location", "test-cluster"),
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "no update when synctargets is scheduled",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: newPlacement("test-placement", "test-location", "test-cluster"),
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "synctargets becomes not ready",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: newPlacement("test-placement", "test-location", ""),
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "select a new synctarget",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: newPlacement("test-placement", "test-location", "test-cluster-2"),
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQA9mRmZ5RuT9vKRZokxZTm1Yk9SqKyfOMoTEr": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "scheduled cluster is removing",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": now3339,
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: newPlacement("test-placement", "test-location", "test-cluster"),
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "remove clusters which is removing after grace period",
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": time.Now().Add(-1 * (removingGracePeriod + 1)).UTC().Format(time.RFC3339),
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "34sZi3721YwBLDHUuNVIOLxuYp5nEZBpsTQyDq": string(workloadv1alpha1.ResourceStateSync),
			},
			placement: newPlacement("test-placement", "test-location", ""),
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

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:  listPlacement,
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
		name       string
		placements []*schedulingv1alpha1.Placement

		labels      map[string]string
		annotations map[string]string

		wantPatch           bool
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "schedule to two location",
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1", "c1"),
				newPlacement("p2", "loc2", "c2"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "placement select the same location",
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1", "c1"),
				newPlacement("p2", "loc1", "c1"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are scheduled already",
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1", "c1"),
				newPlacement("p2", "loc2", "c2"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: false,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": string(workloadv1alpha1.ResourceStateSync),
			},
		},
		{
			name: "cluster are rescheduled when removing",
			placements: []*schedulingv1alpha1.Placement{
				newPlacement("p1", "loc1", "c3"),
				newPlacement("p2", "loc2", "c4"),
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": now3339,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": now3339,
			},
			labels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": string(workloadv1alpha1.ResourceStateSync),
			},
			wantPatch: true,
			expectedAnnotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": now3339,
				workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": now3339,
			},
			expectedLabels: map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aQtdeEWVcqU7h7AKnYMm3KRQ96U4oU2W04yeOa": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "aPkhvUbGK0xoZIjMnM2pA0AuV1g7i4tBwxu5m4": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "5iSfzYTm7pPirj6HKlmfvXMb6AuqSBxNB7vkVP": string(workloadv1alpha1.ResourceStateSync),
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "8s5f69zIcmjG486nG2jBF8BdYtgwPS7PVP1bTL": string(workloadv1alpha1.ResourceStateSync),
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

			var patched bool
			reconciler := &placementSchedulingReconciler{
				listPlacement:  listPlacement,
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
