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

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestIndexAPIExportEndpointSliceByAPIExport(t *testing.T) {
	tests := map[string]struct {
		obj     interface{}
		want    []string
		wantErr bool
	}{
		"not an APIExportEndpointSlice": {
			obj:     "not an APIExportEndpointSlice",
			want:    []string{},
			wantErr: true,
		},
		"valid APIExportEndpointSlice": {
			obj: &apisv1alpha1.APIExportEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:local",
					},
					Name: "foo",
				},
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:default",
						Name: "foo",
					},
				},
			},
			want: []string{
				logicalcluster.NewPath("root:default:foo").String(),
				logicalcluster.NewPath("root:local:foo").String(),
			},
			wantErr: false,
		},
		"valid APIExportEndpointSlice local to export": {
			obj: &apisv1alpha1.APIExportEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:local",
					},
					Name: "foo",
				},
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Name: "foo",
					},
				},
			},
			want: []string{
				logicalcluster.NewPath("root:local:foo").String(),
			},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := IndexAPIExportEndpointSliceByAPIExport(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("IndexAPIExportEndpointSliceByAPIExport() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("IndexAPIExportEndpointSliceByAPIExport() got = %v, want %v", got, tt.want)
			}
		})
	}
}
