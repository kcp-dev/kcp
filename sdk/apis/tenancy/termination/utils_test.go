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

package termination

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestMergeFinalizersUnique(t *testing.T) {
	tests := []struct {
		name string
		fin  []corev1alpha1.LogicalClusterFinalizer
		finS []string
		exp  []string
	}{
		{
			name: "finalizer nil",
			fin:  nil,
			finS: []string{"1", "2"},
			exp:  []string{"1", "2"},
		},
		{
			name: "strings nil",
			fin:  []corev1alpha1.LogicalClusterFinalizer{"1", "2"},
			finS: nil,
			exp:  []string{"1", "2"},
		},
		{
			name: "both non nil",
			fin:  []corev1alpha1.LogicalClusterFinalizer{"1", "3"},
			finS: []string{"2", "4"},
			exp:  []string{"1", "2", "3", "4"},
		},
		{
			name: "overlapping values",
			fin:  []corev1alpha1.LogicalClusterFinalizer{"1", "2"},
			finS: []string{"1", "2"},
			exp:  []string{"1", "2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MergeFinalizersUnique(tc.fin, tc.finS)
			if diff := cmp.Diff(res, tc.exp); diff != "" {
				t.Errorf("exp no diff, got %s", diff)
			}
		})
	}
}
