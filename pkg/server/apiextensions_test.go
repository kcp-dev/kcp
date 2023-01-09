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

	"github.com/stretchr/testify/require"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdgroups"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestSystemCRDsLogicalClusterName(t *testing.T) {
	require.Equal(t, SystemCRDClusterName.String(), reservedcrdgroups.SystemCRDLogicalClusterName, "reservedcrdgroups admission check should match SystemCRDLogicalCluster")
}

func TestDecorateCRDWithBinding(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name               string
		crd                *apiextensionsv1.CustomResourceDefinition
		deleteTime         *metav1.Time
		identity           string
		expectedConditions []apiextensionsv1.CustomResourceDefinitionCondition
		expectedAnnotation map[string]string
	}{
		{
			name: "update annotation only",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"foo": "bar",
					},
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
						{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
					},
				},
			},
			deleteTime: nil,
			identity:   "bob",
			expectedConditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
			},
			expectedAnnotation: map[string]string{
				apisv1alpha1.AnnotationAPIIdentityKey: "bob",
				"foo":                                 "bar",
			},
		},
		{
			name: "apibinding is deleting",
			crd: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"foo": "bar",
					},
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
						{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
					},
				},
			},
			deleteTime: &now,
			identity:   "bob",
			expectedConditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
				{Type: apiextensionsv1.Terminating, Status: apiextensionsv1.ConditionTrue},
			},
			expectedAnnotation: map[string]string{
				apisv1alpha1.AnnotationAPIIdentityKey: "bob",
				"foo":                                 "bar",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crdCopy := tt.crd.DeepCopy()

			newCrd := decorateCRDWithBinding(crdCopy, tt.identity, tt.deleteTime)

			if !equality.Semantic.DeepEqual(tt.crd, crdCopy) {
				t.Errorf("expect crd not mutated, but got %v", crdCopy)
			}

			for _, expCondition := range tt.expectedConditions {
				cond := apiextensionshelpers.FindCRDCondition(newCrd, expCondition.Type)
				if cond == nil {
					t.Fatalf("Missing status condition %v", expCondition.Type)
				}

				if cond.Status != expCondition.Status {
					t.Errorf("expect condition status %q, got %q for type %s", expCondition.Status, cond.Status, cond.Type)
				}

				if cond.Reason != expCondition.Reason {
					t.Errorf("expect condition reason %q, got %q for type %s", expCondition.Reason, cond.Reason, cond.Type)
				}
			}

			if !equality.Semantic.DeepEqual(newCrd.Annotations, tt.expectedAnnotation) {
				t.Errorf("expect annotion %v, got %v", tt.expectedAnnotation, newCrd.Annotations)
			}

			if !newCrd.DeletionTimestamp.Equal(tt.deleteTime) {
				t.Errorf("expect deletetime %v, got %v", tt.deleteTime, newCrd.DeletionTimestamp)
			}
		})
	}
}

func TestShallowCopyAndMakePartialMetadataCRD(t *testing.T) {
	original := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Description: "desc",
							Type:        "bob",
						},
					},
				},
			},
		},
	}

	shallow := shallowCopyCRDAndDeepCopyAnnotations(original)
	addPartialMetadataCRDAnnotation(shallow)

	// Validate that we've added the annotation in the shallow copy.
	_, ok := shallow.Annotations[annotationKeyPartialMetadata]
	require.True(t, ok)

	// Validate that the original still has description and type intact.
	_, ok = original.Annotations[annotationKeyPartialMetadata]
	require.False(t, ok)

	if original.Spec.Versions[0].Schema.OpenAPIV3Schema.Description != "desc" {
		t.Errorf("expected shallow copy to not modify original schema description")
	}
	if original.Spec.Versions[0].Schema.OpenAPIV3Schema.Type != "bob" {
		t.Error("expected shallow copy to not modify original schema type")
	}
}
