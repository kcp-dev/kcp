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

package shared

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGetUpstreamResourceName(t *testing.T) {

	tests := []struct {
		name     string
		gvr      schema.GroupVersionResource
		resource string
		want     string
	}{
		{
			name:     "kcp-root-ca.crt configmap, should be translated to kube-root-ca.crt",
			gvr:      schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
			resource: "kcp-root-ca.crt",
			want:     "kube-root-ca.crt",
		},
		{
			name:     "not kcp-root-ca.crt configmap, should not be translated",
			gvr:      schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
			resource: "my-configmap",
			want:     "my-configmap",
		},
		{
			name:     "a default token secret with kcp prefix, should be translated",
			gvr:      schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
			resource: "kcp-default-token-1234",
			want:     "default-token-1234",
		},
		{
			name:     "a non default token secret without kcp prefix, should not be translated",
			gvr:      schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
			resource: "my-super-secret",
			want:     "my-super-secret",
		},
		{
			name:     "a different GVR than configmap or secret, should not be translated",
			gvr:      schema.GroupVersionResource{Group: "random", Version: "v1", Resource: "another"},
			resource: "kcp-foo",
			want:     "kcp-foo",
		},
		{
			name:     "a configmap with a kcp prefix, shouldn't be translated",
			gvr:      schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"},
			resource: "kcp-default-token-1234",
			want:     "kcp-default-token-1234",
		},
		{
			name:     "invalid GVR, should not be translated",
			gvr:      schema.GroupVersionResource{Group: "", Version: "", Resource: ""},
			resource: "kcp-foo",
			want:     "kcp-foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetUpstreamResourceName(tt.gvr, tt.resource); got != tt.want {
				t.Errorf("GetUpstreamResourceName() = %v, want %v", got, tt.want)
			}
		})
	}
}
