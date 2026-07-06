/*
Copyright 2022 The kcp Authors.

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

package apiresourceschema

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
)

func createAttr(s *apisv1alpha1.APIResourceSchema) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(s),
		nil,
		apisv1alpha1.Kind("APIResourceSchema").WithVersion("v1alpha1"),
		"",
		s.Name,
		apisv1alpha1.Resource("apiresourceschemas").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		attr           admission.Attributes
		expectedErrors []string
	}{
		{
			name: "an APIResourceSchema can pass admission",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.cowboys.wild.west
spec:
  group: wild.west
  names:
    plural: cowboys
    singular: cowboy
    kind: Cowboy
    listKind: CowboyList
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    subresources:
      status: {}
    schema:
      type: object
      properties:
        spec:
          type: object
            `)),
		},
		{
			name: "an APIResourceSchema can fail admission",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: may.2022.cowboys.wild.west
spec:
  group: wild.west
  scope: Clustered
  names:
	plural: cowboys
  versions:
  - name: v1-rc17
    served: true
    storage: false
  - name: v2
    served: true
    storage: true
	schema:
	  type: thing
            `)),
			expectedErrors: []string{
				"metadata.name: Invalid value: \"may.2022.cowboys.wild.west\": must match ^[a-z]([-a-z0-9]*[a-z0-9])?$ in front of .cowboys.wild.west",
				"spec.versions[0].schema: Required value",
				"spec.versions[1].schema.openAPIV3Schema.type: Unsupported value: \"thing\": supported values: \"array\", \"boolean\", \"integer\", \"number\", \"object\", \"string\"",
				"spec.versions[1].schema.openAPIV3Schema.type: Invalid value: \"thing\": must be object at the root",
				"spec.names.singular: Required value",
				"spec.names.kind: Required value",
				"spec.names.listKind: Required value",
			},
		},
		{
			name: "an APIResourceSchema can define a core group resource",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.pods.core
spec:
  names:
    plural: pods
    singular: pod
    kind: Pod
    listKind: PodList
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      type: object
            `)),
		},
		{
			name: "core group is rejected, use empty string",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.pods.core
spec:
  group: core
  names:
    plural: pods
    singular: pod
    kind: Pod
    listKind: PodList
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      type: object
            `)),
			expectedErrors: []string{
				"spec.group: Invalid value: \"core\": must be empty string for the core group",
			},
		},
		{
			name: "selectableFields are accepted",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.cowboys.wild.west
spec:
  group: wild.west
  names:
    plural: cowboys
    singular: cowboy
    kind: Cowboy
    listKind: CowboyList
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      type: object
      properties:
        spec:
          type: object
          properties:
            color:
              type: string
    selectableFields:
    - jsonPath: .spec.color
            `)),
		},
		{
			name: "too many selectableFields are rejected",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.cowboys.wild.west
spec:
  group: wild.west
  names:
    plural: cowboys
    singular: cowboy
    kind: Cowboy
    listKind: CowboyList
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      type: object
      properties:
        spec:
          type: object
          properties:
            c0: {type: string}
            c1: {type: string}
            c2: {type: string}
            c3: {type: string}
            c4: {type: string}
            c5: {type: string}
            c6: {type: string}
            c7: {type: string}
            c8: {type: string}
    selectableFields:
    - jsonPath: .spec.c0
    - jsonPath: .spec.c1
    - jsonPath: .spec.c2
    - jsonPath: .spec.c3
    - jsonPath: .spec.c4
    - jsonPath: .spec.c5
    - jsonPath: .spec.c6
    - jsonPath: .spec.c7
    - jsonPath: .spec.c8
            `)),
			expectedErrors: []string{
				"spec.versions[0].selectableFields: Too many: 9: must have at most 8 items",
			},
		},
		{
			name: "selectableFields without a schema are rejected",
			attr: createAttr(unmarshalOrDie(`
apiVersion: apis.kcp.sh/v1alpha1
kind: APIResourceSchema
metadata:
  name: july.cowboys.wild.west
spec:
  group: wild.west
  names:
    plural: cowboys
    singular: cowboy
    kind: Cowboy
    listKind: CowboyList
  scope: Cluster
  versions:
  - name: v1
    served: true
    storage: true
    selectableFields:
    - jsonPath: .spec.color
            `)),
			expectedErrors: []string{
				"spec.versions[0].schema: Required value",
				"spec.versions[0].selectableFields: Invalid value: \"\": may only be set when `version.schema.openAPIV3Schema` is not included",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &apiResourceSchemaValidation{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})
			err := o.Validate(ctx, tt.attr, nil)
			wantErr := len(tt.expectedErrors) > 0
			if (err != nil) != wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, wantErr)
			}
			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tt.expectedErrors {
					if !strings.Contains(err.Error(), expected) {
						t.Errorf("expected error %q", expected)
					}
				}
			}
		})
	}
}

func unmarshalOrDie(yml string) *apisv1alpha1.APIResourceSchema {
	s := apisv1alpha1.APIResourceSchema{}
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(yml, "\t", "    ")), &s); err != nil {
		panic(err)
	}
	return &s
}
