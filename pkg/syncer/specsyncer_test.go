/*
Copyright 2021 The KCP Authors.

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

package syncer

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestDeepEqualApartFromStatus(t *testing.T) {
	type args struct {
		a, b *unstructured.Unstructured
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "both objects are equals",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals as are being deleted",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals even they have different statuses",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"status": map[string]interface{}{},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"status": map[string]interface{}{
							"phase": "Failed",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals even they have a different value in InternalClusterStatusAnnotationPrefix",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								workloadv1alpha1.InternalClusterStatusAnnotationPrefix: "2",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								workloadv1alpha1.InternalClusterStatusAnnotationPrefix: "1",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "not equal as b is missing labels",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b has different labels values",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "another_test_value",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource is missing annotations",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation": "this is an annotation",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource has different annotations",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation": "this is an annotation",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation":  "this is an annotation",
								"annotation2": "this is another annotation",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource has finalizers",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal even objects are equal as A is being deleted",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":              "test_name",
							"namespace":         "test_namespace",
							"deletionTimestamp": "2010-11-10T23:00:00Z",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"test_annotation": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"test_annotation": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b has additional fields than a",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"other_key": "other_value",
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deepEqualApartFromStatus(tt.args.a, tt.args.b); got != tt.want {
				t.Errorf("deepEqualApartFromStatus() = %v, want %v", got, tt.want)
			}
		})
	}

}
