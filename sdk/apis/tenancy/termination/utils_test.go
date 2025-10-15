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

func TestMergeTerminatorsUnique(t *testing.T) {
	tests := []struct {
		name  string
		term  []corev1alpha1.LogicalClusterTerminator
		termS []string
		exp   []string
	}{
		{
			name:  "terminator nil",
			term:  nil,
			termS: []string{"1", "2"},
			exp:   []string{"1", "2"},
		},
		{
			name:  "strings nil",
			term:  []corev1alpha1.LogicalClusterTerminator{"1", "2"},
			termS: nil,
			exp:   []string{"1", "2"},
		},
		{
			name:  "both non nil",
			term:  []corev1alpha1.LogicalClusterTerminator{"1", "3"},
			termS: []string{"2", "4"},
			exp:   []string{"1", "2", "3", "4"},
		},
		{
			name:  "overlapping values",
			term:  []corev1alpha1.LogicalClusterTerminator{"1", "2"},
			termS: []string{"1", "2"},
			exp:   []string{"1", "2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MergeTerminatorsUnique(tc.term, tc.termS)
			if diff := cmp.Diff(res, tc.exp); diff != "" {
				t.Errorf("exp no diff, got %s", diff)
			}
		})
	}
}
