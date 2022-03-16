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

package apibinding

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clusters"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestIndexAPIBindingByWorkspaceExport(t *testing.T) {
	tests := map[string]struct {
		obj     interface{}
		want    []string
		wantErr bool
	}{
		"not an APIBinding": {
			obj:     "not an APIBinding",
			want:    []string{},
			wantErr: true,
		},
		"no workspace reference": {
			obj:     &apisv1alpha1.APIBinding{},
			want:    []string{},
			wantErr: false,
		},
		"has a workspace reference": {
			obj: &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: "root:default",
					Name:        "foo",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							WorkspaceName: "workspace1",
							ExportName:    "export1",
						},
					},
				},
			},
			want:    []string{clusters.ToClusterAwareKey("root:workspace1", "export1")},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := indexAPIBindingByWorkspaceExport(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("indexAPIBindingByWorkspaceExport() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("indexAPIBindingByWorkspaceExport() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIndexAPIExportByAPIResourceSchemas(t *testing.T) {
	tests := map[string]struct {
		obj     interface{}
		want    []string
		wantErr bool
	}{
		"not an APIExport": {
			obj:     "not an APIExport",
			want:    []string{},
			wantErr: true,
		},
		"valid APIExport": {
			obj: &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: "root:default",
					Name:        "foo",
				},
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"schema1", "some-other-schema"},
				},
			},
			want: []string{
				clusters.ToClusterAwareKey("root:default", "schema1"),
				clusters.ToClusterAwareKey("root:default", "some-other-schema"),
			},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := indexAPIExportByAPIResourceSchemas(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("indexAPIExportByAPIResourceSchemas() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("indexAPIExportByAPIResourceSchemas() got = %v, want %v", got, tt.want)
			}
		})
	}
}
