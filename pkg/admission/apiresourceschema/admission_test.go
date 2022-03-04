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

package apiresourceschema

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(s *apisv1alpha1.APIResourceSchema) admission.Attributes {
	return admission.NewAttributesRecord(
		s,
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

// nolint:deadcode,unused
func updateAttr(s, old *apisv1alpha1.APIResourceSchema) admission.Attributes {
	return admission.NewAttributesRecord(
		s,
		old,
		tenancyv1alpha1.Kind("APIResourceSchema").WithVersion("v1alpha1"),
		"",
		s.Name,
		tenancyv1alpha1.Resource("apiresourceschemas").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
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
				"metadata.name: Invalid value: \"may.2022.cowboys.wild.west\": must match patch ^[a-z]([-a-z0-9]*[a-z0-9])? in front of .<resource>.<group>",
				"spec.versions[0].schema: Required value: schemas are required",
				"spec.versions[1].schema.openAPIV3Schema.type: Unsupported value: \"thing\": supported values: \"array\", \"boolean\", \"integer\", \"number\", \"object\", \"string\"",
				"spec.versions[1].schema.openAPIV3Schema.type: Invalid value: \"thing\": must be object at the root",
				"spec.names.singular: Required value",
				"spec.names.kind: Required value",
				"spec.names.listKind: Required value",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
