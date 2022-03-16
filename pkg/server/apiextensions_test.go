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

package server

import (
	"testing"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

func TestWorkspaceKey(t *testing.T) {
	tests := []struct {
		name string
		org  string
		ws   string
		want string
	}{
		{
			name: "org ws",
			org:  helper.RootCluster,
			ws:   "myws",
			want: "root#$#myws",
		},
		{
			name: "normal ws",
			org:  "myorg",
			ws:   "myws",
			want: "root:myorg#$#myws",
		},
		{
			name: "root ws",
			org:  "",
			ws:   helper.RootCluster,
			want: helper.RootCluster,
		},
		{
			name: "fake root ws",
			org:  "myorg",
			ws:   "root",
			want: "root:myorg#$#root",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspaceKey(tt.org, tt.ws); got != tt.want {
				t.Errorf("WorkspaceKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
