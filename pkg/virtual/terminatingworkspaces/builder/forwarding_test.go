/*
Copyright 2025 The KCP Authors.

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

package builder

import (
	"context"
	"testing"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestValidateOnlyTerminatorChanged(t *testing.T) {
	tests := []struct {
		name       string
		expErr     bool
		terminator string
		old        runtime.Object
		new        runtime.Object
	}{
		{
			name:       "remove owned terminator",
			expErr:     false,
			terminator: "t1",
			old: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{
						"t1",
						"t2",
					},
				},
			},
			new: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{
						"t2",
					},
				},
			},
		},
		{
			name:       "remove non-owned terminator",
			expErr:     true,
			terminator: "t1",
			old: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{
						"t1",
						"t2",
					},
				},
			},
			new: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{
						"t1",
					},
				},
			},
		},
		{
			name:   "no object changes",
			expErr: true, // we expect an error here, as we always expect the number of terminators to decrease
			old:    &corev1alpha1.LogicalCluster{},
			new:    &corev1alpha1.LogicalCluster{},
		},
	}

	// swallow any log output, so we don't pollute test results
	ctx := logr.NewContext(context.Background(), logr.Discard())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// mimic the real unstructured.Unstructured objects which are coming into the validateTerminatorsUpdate funcs
			oldMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.old)
			if err != nil {
				t.Error(err)
			}
			newMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.new)
			if err != nil {
				t.Error(err)
			}

			err = validateTerminatorStatusUpdate(corev1alpha1.LogicalClusterTerminator(tc.terminator), "test-cluster")(ctx, &unstructured.Unstructured{Object: newMap}, &unstructured.Unstructured{Object: oldMap})
			if !tc.expErr && err != nil {
				t.Errorf("expected no error, but got %q", err)
			} else if tc.expErr && err == nil {
				t.Errorf("expected an error, but got none")
			}
		})
	}
}
