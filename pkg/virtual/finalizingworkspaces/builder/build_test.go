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
	"fmt"
	"testing"
)

func TestIsDiscoveryRequest(t *testing.T) {
	tt := []struct {
		path string
		exp  bool
	}{
		{"/api", true},
		{"/api/v1", true},
		{"/api/somegroup", true},
		{"/apis/somegroup", true},
		{"/apis/somegroup/v1", true},
		{"/healthz", false},
		{"/", false},
		{"/api/v1/namespace", false},
		{"/apis/somegroup/v1/namespaces", false},
	}
	for _, tc := range tt {
		t.Run(fmt.Sprintf("%q", tc.path), func(t *testing.T) {
			if res := isDiscoveryRequest(tc.path); res != tc.exp {
				t.Errorf("Exp %t, got %t", tc.exp, res)
			}
		})
	}
}
