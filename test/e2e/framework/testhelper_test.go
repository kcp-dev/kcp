/*
Copyright 2021 The KCP Authors.

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

package framework

import (
	"testing"
)

func TestSanitizeString(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		expected string
	}{
		{
			name:     "simple, no changes",
			in:       "my_golden.yaml",
			expected: "zz_fixture_my_golden.yaml",
		},
		{
			name:     "complex",
			in:       "my_Go\\l'de`n.yaml",
			expected: "zz_fixture_my_Go_l_de_n.yaml",
		},
		{
			name:     "no double underscores",
			in:       "a_|",
			expected: "zz_fixture_a_",
		},
		{
			name:     "numbers are kept",
			in:       "0123456789.yaml",
			expected: "zz_fixture_0123456789.yaml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if result := sanitizeFilename(tc.in); result != tc.expected {
				t.Errorf("expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}
