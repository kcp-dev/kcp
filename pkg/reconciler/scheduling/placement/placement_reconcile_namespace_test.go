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
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"
)

func TestPlacementPhase(t *testing.T) {

	testCases := []struct {
		name              string
		ns                *corev1.Namespace
		phase             schedulingv1alpha1.PlacementPhase
		namespaceSelector *metav1.LabelSelector
		selectedLocation  *schedulingv1alpha1.LocationReference
		expectedPhase     schedulingv1alpha1.PlacementPhase
	}{
		{
			name:          "placement is pending",
			phase:         schedulingv1alpha1.PlacementPending,
			expectedPhase: schedulingv1alpha1.PlacementPending,
		},
		{
			name:          "placement has no selected location",
			phase:         schedulingv1alpha1.PlacementUnbound,
			expectedPhase: schedulingv1alpha1.PlacementPending,
		},
		{
			name:  "namespace does not have placement annotation",
			phase: schedulingv1alpha1.PlacementUnbound,
			selectedLocation: &schedulingv1alpha1.LocationReference{
				Path:         "root",
				LocationName: "test-location",
			},
			expectedPhase: schedulingv1alpha1.PlacementUnbound,
		},
		{
			name:  "namespace does not bound to this placement",
			phase: schedulingv1alpha1.PlacementBound,
			ns:    &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "testns"}},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				Path:         "root",
				LocationName: "test-location",
			},
			expectedPhase: schedulingv1alpha1.PlacementUnbound,
		},
		{
			name:  "namespace bound to this placement, but not select by placement",
			phase: schedulingv1alpha1.PlacementBound,
			ns:    &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "testns"}},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				Path:         "root",
				LocationName: "test-location",
			},
			expectedPhase: schedulingv1alpha1.PlacementUnbound,
		},
		{
			name:              "namespace bound to this placement",
			phase:             schedulingv1alpha1.PlacementUnbound,
			namespaceSelector: &metav1.LabelSelector{},
			ns:                &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "testns"}},
			selectedLocation: &schedulingv1alpha1.LocationReference{
				Path:         "root",
				LocationName: "test-location",
			},
			expectedPhase: schedulingv1alpha1.PlacementBound,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testPlacement := &schedulingv1alpha1.Placement{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-placement",
				},
				Spec: schedulingv1alpha1.PlacementSpec{
					NamespaceSelector: testCase.namespaceSelector,
				},
				Status: schedulingv1alpha1.PlacementStatus{
					Phase:            testCase.phase,
					SelectedLocation: testCase.selectedLocation,
				},
			}
			listNamespacesWithAnnotation := func(clusterName logicalcluster.Name) ([]*corev1.Namespace, error) {
				if testCase.ns == nil {
					return []*corev1.Namespace{}, nil
				}
				return []*corev1.Namespace{testCase.ns}, nil
			}

			reconciler := &placementNamespaceReconciler{listNamespacesWithAnnotation: listNamespacesWithAnnotation}

			_, updated, err := reconciler.reconcile(context.TODO(), testPlacement)
			require.NoError(t, err)
			require.Equal(t, updated.Status.Phase, testCase.expectedPhase)
		})
	}
}
