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

package indexers

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

func TestIndexWorkspaceByURL(t *testing.T) {
	tests := map[string]struct {
		obj     interface{}
		want    string
		wantErr bool
	}{
		"not a Workspace": {
			obj:     "not a Workspace",
			want:    "",
			wantErr: true,
		},
		"valid Workspace without url": {
			obj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			},
			wantErr: false,
			want:    "",
		},
		"valid Workspace with url": {
			obj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "foo",
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL: "https://example.com",
				},
			},
			wantErr: false,
			want:    "https://example.com",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := IndexWorkspaceByURL(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("IndexWorkspaceByURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("IndexWorkspaceByURL() got = %v, want %v", got, tt.want)
			}
		})
	}
}
