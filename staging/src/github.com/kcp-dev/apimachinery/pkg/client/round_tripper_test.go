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

package client

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestRoundTripper_generatePath(t *testing.T) {
	tests := map[string]struct {
		originalPath string
		desired      string
	}{
		"empty path": {
			desired: "/clusters/root:org:ws",
		},
		"single segment prefix with slashes on both ends": {
			originalPath: "/prefix/",
			desired:      "/clusters/root:org:ws/prefix/",
		},
		"multiple segments prefix": {
			originalPath: "/several/divisions/of/prefix",
			desired:      "/clusters/root:org:ws/several/divisions/of/prefix",
		},
		"single segment prefix with slash at beginning only": {
			originalPath: "/prefix",
			desired:      "/clusters/root:org:ws/prefix",
		},
		"single segment prefix with no slashes": {
			originalPath: "prefix",
			desired:      "/clusters/root:org:ws/prefix",
		},
		"/api": {
			originalPath: "/api",
			desired:      "/clusters/root:org:ws/api",
		},
		"/api/v1": {
			originalPath: "/api/v1",
			desired:      "/clusters/root:org:ws/api/v1",
		},
		"/apis": {
			originalPath: "/apis",
			desired:      "/clusters/root:org:ws/apis",
		},
		"/apis/example.io": {
			originalPath: "/apis/example.io",
			desired:      "/clusters/root:org:ws/apis/example.io",
		},
		"Path already added /clusters/root:org:ws/apis/example.io": {
			originalPath: "/clusters/root:org:ws/apis/example.io",
			desired:      "/clusters/root:org:ws/apis/example.io",
		},
		"sample APIExport virtual workspace URL with apis": {
			originalPath: "/services/apiexport/root:default:pub/some-export/apis/foo.io/v1alpha1/widgets",
			desired:      "/services/apiexport/root:default:pub/some-export/clusters/root:org:ws/apis/foo.io/v1alpha1/widgets",
		},
		"sample APIExport virtual workspace URL with api": {
			originalPath: "/services/apiexport/root:default:pub/some-export/api/v1/configmaps",
			desired:      "/services/apiexport/root:default:pub/some-export/clusters/root:org:ws/api/v1/configmaps",
		},
	}
	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			result := generatePath(tt.originalPath, logicalcluster.NewPath("root:org:ws"))
			if result != tt.desired {
				t.Errorf("got %v, want %v", result, tt.desired)
			}
		})
	}
}
