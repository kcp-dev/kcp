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
	"fmt"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

func TestPlacementScheduling(t *testing.T) {
	testCases := []struct {
		name              string
		locationSelectors []metav1.LabelSelector
		locations         []*schedulingv1alpha1.Location
		phase             schedulingv1alpha1.PlacementPhase
		selectedLocation  *schedulingv1alpha1.LocationReference

		listLocationsError error

		wantError          bool
		wantPhase          schedulingv1alpha1.PlacementPhase
		wantSelectLocation *schedulingv1alpha1.LocationReference
		wantStatus         corev1.ConditionStatus
	}{
		{
			name:       "no locations",
			phase:      schedulingv1alpha1.PlacementPending,
			wantPhase:  schedulingv1alpha1.PlacementPending,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name:  "bound location to the placement",
			phase: schedulingv1alpha1.PlacementPending,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "aws",
					},
				},
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
				newLocation("gcp", map[string]string{"cloud": "gcp"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementUnbound,
			wantStatus: corev1.ConditionTrue,
			wantSelectLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
		},
		{
			name:  "update location to the placement",
			phase: schedulingv1alpha1.PlacementUnbound,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "gcp",
					},
				},
			},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
				newLocation("gcp", map[string]string{"cloud": "gcp"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementUnbound,
			wantStatus: corev1.ConditionTrue,
			wantSelectLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "gcp",
			},
		},
		{
			name:  "stick location when the placement is bound",
			phase: schedulingv1alpha1.PlacementBound,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "aws",
					},
				},
			},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
				newLocation("aws-1", map[string]string{"cloud": "aws"}),
				newLocation("aws-2", map[string]string{"cloud": "aws"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementBound,
			wantStatus: corev1.ConditionTrue,
			wantSelectLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
		},
		{
			name:  "change location when the placement is bound",
			phase: schedulingv1alpha1.PlacementBound,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "gcp",
					},
				},
			},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementBound,
			wantStatus: corev1.ConditionFalse,
			wantSelectLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
		},
		{
			name:  "no valid location when the placement is unbound",
			phase: schedulingv1alpha1.PlacementUnbound,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "gcp",
					},
				},
			},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementPending,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name:  "get location error",
			phase: schedulingv1alpha1.PlacementUnbound,
			locationSelectors: []metav1.LabelSelector{
				{
					MatchLabels: map[string]string{
						"cloud": "aws",
					},
				},
			},
			listLocationsError: fmt.Errorf("list location fails"),
			selectedLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			locations: []*schedulingv1alpha1.Location{
				newLocation("aws", map[string]string{"cloud": "aws"}),
			},
			wantPhase:  schedulingv1alpha1.PlacementUnbound,
			wantStatus: corev1.ConditionFalse,
			wantSelectLocation: &schedulingv1alpha1.LocationReference{
				LocationName: "aws",
			},
			wantError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testPlacement := &schedulingv1alpha1.Placement{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-placement",
				},
				Spec: schedulingv1alpha1.PlacementSpec{
					LocationSelectors: testCase.locationSelectors,
				},
				Status: schedulingv1alpha1.PlacementStatus{
					SelectedLocation: testCase.selectedLocation,
					Phase:            testCase.phase,
				},
			}

			listLocation := func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error) {
				return testCase.locations, testCase.listLocationsError
			}

			reconciler := &placementReconciler{listLocationsByPath: listLocation}
			_, updated, err := reconciler.reconcile(context.TODO(), testPlacement)

			if testCase.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, testCase.wantPhase, updated.Status.Phase)
			c := conditions.Get(updated, schedulingv1alpha1.PlacementReady)
			require.NotNil(t, c)
			require.Equal(t, testCase.wantStatus, c.Status)
			require.Equal(t, testCase.wantSelectLocation, updated.Status.SelectedLocation)

		})
	}
}

func newLocation(name string, labels map[string]string) *schedulingv1alpha1.Location {
	return &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}
