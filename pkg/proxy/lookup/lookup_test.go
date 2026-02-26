/*
Copyright 2026 The kcp Authors.

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

package lookup

import "testing"

func TestExtractClusterFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "virtual workspace URL with cluster",
			path:     "/services/contentconfigurations/clusters/root:orgs:bob/apis/ui.platform-mesh.io/v1alpha1/contentconfigurations",
			expected: "root:orgs:bob",
		},
		{
			name:     "virtual workspace URL with nested cluster path",
			path:     "/services/marketplace/clusters/root:orgs:bob:quickstart/apis/v1/namespaces",
			expected: "root:orgs:bob:quickstart",
		},
		{
			name:     "cluster path only",
			path:     "/clusters/root:orgs:bob/api/v1/namespaces",
			expected: "root:orgs:bob",
		},
		{
			name:     "cluster at end of path without trailing slash",
			path:     "/services/foo/clusters/root:orgs:bob",
			expected: "root:orgs:bob",
		},
		{
			name:     "no cluster in path",
			path:     "/services/contentconfigurations/apis/v1/namespaces",
			expected: "",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
		{
			name:     "wildcard cluster",
			path:     "/services/foo/clusters/*/apis/v1",
			expected: "*",
		},
		{
			name:     "root cluster only",
			path:     "/services/foo/clusters/root/apis/v1",
			expected: "root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractClusterFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("extractClusterFromPath(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
