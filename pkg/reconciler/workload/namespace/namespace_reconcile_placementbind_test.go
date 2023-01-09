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
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

func TestBindPlacement(t *testing.T) {
	testCases := []struct {
		name              string
		placementPhase    schedulingv1alpha1.PlacementPhase
		isReady           bool
		labels            map[string]string
		annotations       map[string]string
		namespaceSelector *metav1.LabelSelector

		expectedAnnotation map[string]string
		wantPatch          bool
	}{
		{
			name:           "placement is pending",
			placementPhase: schedulingv1alpha1.PlacementPending,
			isReady:        true,
		},
		{
			name:           "placement is not ready",
			placementPhase: schedulingv1alpha1.PlacementBound,
			isReady:        false,
		},
		{
			name:           "placement does not select the namespace",
			placementPhase: schedulingv1alpha1.PlacementBound,
			isReady:        true,
			labels: map[string]string{
				"foor": "bar",
			},
			namespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo1": "bar1"},
			},
		},
		{
			name:              "choose a placement",
			placementPhase:    schedulingv1alpha1.PlacementBound,
			isReady:           true,
			namespaceSelector: &metav1.LabelSelector{},
			wantPatch:         true,
			expectedAnnotation: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
		},
		{
			name:           "do not patch if there is existing placement annotation",
			placementPhase: schedulingv1alpha1.PlacementBound,
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
			isReady:           true,
			namespaceSelector: &metav1.LabelSelector{},
			wantPatch:         false,
			expectedAnnotation: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: "",
			},
		},
		{
			name:           "update if existing placement is not ready",
			placementPhase: schedulingv1alpha1.PlacementBound,
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: `{"test-placement":"Bound"}`,
			},
			isReady:            false,
			namespaceSelector:  &metav1.LabelSelector{},
			wantPatch:          true,
			expectedAnnotation: map[string]string{},
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

			testPlacement := &schedulingv1alpha1.Placement{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-placement",
				},
				Spec: schedulingv1alpha1.PlacementSpec{
					NamespaceSelector: testCase.namespaceSelector,
				},
				Status: schedulingv1alpha1.PlacementStatus{
					Phase: testCase.placementPhase,
				},
			}

			if testCase.isReady {
				conditions.MarkTrue(testPlacement, schedulingv1alpha1.PlacementReady)
			} else {
				conditions.MarkFalse(
					testPlacement, schedulingv1alpha1.PlacementReady, "TestNotReady", conditionsv1alpha1.ConditionSeverityError, "")
			}

			listPlacement := func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Placement, error) {
				return []*schedulingv1alpha1.Placement{testPlacement}, nil
			}

			var patched bool
			reconciler := &bindNamespaceReconciler{
				listPlacement:  listPlacement,
				patchNamespace: patchNamespaceFunc(&patched, ns),
			}

			_, updated, err := reconciler.reconcile(context.TODO(), ns)
			require.NoError(t, err)
			require.Equal(t, testCase.wantPatch, patched)
			require.Equal(t, testCase.expectedAnnotation, updated.Annotations)
		})
	}
}

func patchNamespaceFunc(patched *bool, ns *corev1.Namespace) func(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	return func(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
		*patched = true

		nsData, _ := json.Marshal(ns)
		updatedData, err := jsonpatch.MergePatch(nsData, data)
		if err != nil {
			return nil, err
		}

		var patchedNS corev1.Namespace
		err = json.Unmarshal(updatedData, &patchedNS)
		if err != nil {
			return ns, err
		}

		return &patchedNS, nil
	}
}
