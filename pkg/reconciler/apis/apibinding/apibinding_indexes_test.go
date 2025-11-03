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

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/client"
)

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
			obj: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:default",
					},
					Name: "foo",
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Name:   "schema1",
							Group:  "org",
							Schema: "v1.schema1.org",
							Storage: apisv1alpha2.ResourceSchemaStorage{
								CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
							},
						},
						{
							Name:   "some-other-schema",
							Group:  "org",
							Schema: "v1.some-other-schema.org",
							Storage: apisv1alpha2.ResourceSchemaStorage{
								CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
							},
						},
					},
				},
			},
			want: []string{
				client.ToClusterAwareKey(logicalcluster.NewPath("root:default"), "v1.schema1.org"),
				client.ToClusterAwareKey(logicalcluster.NewPath("root:default"), "v1.some-other-schema.org"),
			},
			wantErr: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := indexAPIExportsByAPIResourceSchemasFunc(tt.obj)
			if (err != nil) != tt.wantErr {
				t.Errorf("indexAPIExportsByAPIResourceSchemasFunc() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("indexAPIExportsByAPIResourceSchemasFunc() got = %v, want %v", got, tt.want)
			}
		})
	}
}
