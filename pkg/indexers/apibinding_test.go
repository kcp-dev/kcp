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

package indexers

import (
	"reflect"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestIndexAPIBindingByAPIExport(t *testing.T) {
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
		"has a export reference": {
			obj: &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default",
					},
					Name: "foo",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Path: "root:workspace1",
							Name: "export1",
						},
					},
				},
			},
			want:    []string{logicalcluster.NewPath("root:workspace1").Join("export1").String()},
			wantErr: false,
		},
		"has a local export reference": {
			obj: &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default",
					},
					Name: "foo",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Name: "export1",
						},
					},
				},
			},
			want:    []string{logicalcluster.NewPath("root:default").Join("export1").String()},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := IndexAPIBindingByAPIExport(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("IndexAPIBindingByAPIExport() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("IndexAPIBindingByAPIExport() got = %v, want %v", got, tt.want)
			}
		})
	}
}
