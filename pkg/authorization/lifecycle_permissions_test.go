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

package authorization

import "testing"

func TestLifecycleProxyPathEscapesWorkspace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		// Escapes: re-entering /services/ (another virtual workspace) through the
		// lifecycle content proxy, which keeps the /clusters/<id>/ prefix.
		{"admin shards via cluster prefix", "/clusters/abc123/services/admin/apis/core.kcp.io/v1alpha1/shards", true},
		{"services bare segment via cluster prefix", "/clusters/abc123/services", true},
		{"services trailing slash via cluster prefix", "/clusters/abc123/services/", true},
		{"nested initializingworkspaces via cluster prefix", "/clusters/abc123/services/initializingworkspaces/x:y/clusters/z/apis/g/v1/r", true},
		{"services without cluster prefix", "/services/admin/apis/core.kcp.io/v1alpha1/shards", true},

		// Allowed: genuine workspace-scoped resource and discovery requests.
		{"resource request", "/clusters/abc123/apis/apis.kcp.io/v1alpha1/apibindings", false},
		{"core resource request", "/clusters/abc123/api/v1/namespaces", false},
		{"discovery apis", "/clusters/abc123/apis", false},
		{"discovery api", "/clusters/abc123/api", false},
		{"openapi", "/clusters/abc123/openapi/v2", false},
		{"cluster prefix only", "/clusters/abc123", false},
		{"cluster prefix only trailing slash", "/clusters/abc123/", false},
		// A resource literally named "services" in a real API group must not be mistaken
		// for the /services/ virtual-workspace plane.
		{"services resource in api group", "/clusters/abc123/api/v1/namespaces/default/services", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := LifecycleProxyPathEscapesWorkspace(tc.path); got != tc.want {
				t.Errorf("LifecycleProxyPathEscapesWorkspace(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
