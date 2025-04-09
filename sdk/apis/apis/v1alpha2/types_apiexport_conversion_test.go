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

package v1alpha2

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"k8s.io/apimachinery/pkg/runtime"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestConvertV1Alpha2APIExports(t *testing.T) {
	testcases := []APIExport{
		{
			Spec: APIExportSpec{},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}, {
					Group:  "bar",
					Name:   "baz",
					Schema: "v1.baz.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	for _, testcase := range testcases {
		t.Run("", func(t *testing.T) {
			v1, err := scheme.ConvertToVersion(&testcase, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}

			v2, err := scheme.ConvertToVersion(v1, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}

			v2.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())

			if changes := cmp.Diff(v2, &testcase); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}

func TestConvertV1Alpha1APIExports(t *testing.T) {
	testcases := []apisv1alpha1.APIExport{
		{
			Spec: apisv1alpha1.APIExportSpec{},
		},
		{
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{
					"v1.foo.org",
					"v1.bar.org",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	for _, testcase := range testcases {
		t.Run("", func(t *testing.T) {
			v2, err := scheme.ConvertToVersion(&testcase, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}

			v1, err := scheme.ConvertToVersion(v2, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}

			v1.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())

			if changes := cmp.Diff(v1, &testcase); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}
