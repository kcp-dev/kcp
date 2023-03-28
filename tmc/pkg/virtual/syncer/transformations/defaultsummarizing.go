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

package transformations

import (
	"encoding/json"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

var _ SummarizingRules = (*DefaultSummarizingRules)(nil)
var _ SummarizingRulesProvider = (*DefaultSummarizingRules)(nil)

// DefaultSummarizingRules provides a default minimal implementation of [SummarizingRules].
// It only adds a status field, which for now is always promoted (see comments below in the code).
type DefaultSummarizingRules struct{}

type field struct {
	FieldPath         string `json:"fieldPath"`
	PromoteToUpstream bool   `json:"PromoteToUpstream,omitempty"`
}

var _ FieldToSummarize = field{}

// Path implements [Field.Path].
func (f field) Path() string {
	return f.FieldPath
}

// Path implements [Field.Path].
func (f field) pathElements() []string {
	return strings.Split(f.FieldPath, ".")
}

// Set implements [Field.Set].
func (f field) Set(resource *unstructured.Unstructured, value interface{}) error {
	return unstructured.SetNestedField(resource.UnstructuredContent(), value, f.pathElements()...)
}

// Get implements [Field.Get].
func (f field) Get(resource *unstructured.Unstructured) (interface{}, bool, error) {
	return unstructured.NestedFieldNoCopy(resource.UnstructuredContent(), f.pathElements()...)
}

// Delete implements [Field.Delete].
func (f field) Delete(resource *unstructured.Unstructured) {
	unstructured.RemoveNestedField(resource.UnstructuredContent(), f.pathElements()...)
}

// CanPromoteToUpstream implements [Field.CanPromoteToUpstream].
func (f field) CanPromoteToUpstream() bool {
	return f.PromoteToUpstream
}

// IsStatus implements [Field.IsStatus].
func (f field) IsStatus() bool {
	elements := f.pathElements()
	return len(elements) == 1 && elements[0] == "status"
}

type fields []field

var _ SummarizingRules = (fields)(nil)

func (fs fields) FieldsToSummarize(gvr schema.GroupVersionResource) []FieldToSummarize {
	result := make([]FieldToSummarize, 0, len(fs))
	for _, f := range fs {
		result = append(result, FieldToSummarize(f))
	}
	return result
}

func (s *DefaultSummarizingRules) SummarizingRulesFor(resource metav1.Object) (SummarizingRules, error) {
	if encoded := resource.GetAnnotations()[v1alpha1.ExperimentalSummarizingRulesAnnotation]; encoded != "" {
		var decoded []field
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return nil, err
		}
		return fields(decoded), nil
	}
	return s, nil
}

func (s *DefaultSummarizingRules) FieldsToSummarize(gvr schema.GroupVersionResource) []FieldToSummarize {
	fields := []FieldToSummarize{
		field{
			FieldPath:         "status",
			PromoteToUpstream: s.canPromoteStatusToUpstream(gvr),
		},
	}

	// TODO(davidfestal): In the future, we would add some well-known fields of some standard types
	// like Service clusterIP:
	//
	//     if gvr == corev1.GenericControlPlaneSchemeGroupVersion.WithResource("services") {
	//         fields = append(fields, field{
	//             path:                 "spec.clusterIP",
	//             canPromoteToUpstream: false,
	//         })
	//     }

	return fields
}

func (s *DefaultSummarizingRules) canPromoteStatusToUpstream(gvr schema.GroupVersionResource) bool {
	switch gvr {
	// TODO(davidfestal): In the future, ingresses and services would have default coordination controllers,
	// we would never promote their status to the upstream resource, since it is inherently related to
	// SyncTarget infrastructure details.
	//
	// case networkingv1.SchemeGroupVersion.WithResource("ingresses"),
	// corev1.GenericControlPlaneSchemeGroupVersion.WithResource("services"):
	//   return false
	default:
		return true
	}
}
