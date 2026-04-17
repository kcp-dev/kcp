/*
Copyright 2026 The kcp Authors.

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

package termination

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestTerminatorToLabel(t *testing.T) {
	for _, testCase := range []corev1alpha1.LogicalClusterTerminator{
		"simple:root:org:ws:whatever",
		"QualifiedName:root:org:ws:whatever",
		"qualified.Name:root:org:ws:whatever",
		"qualified-123-name:root:org:ws:whatever",
		"with.dns/prefix:root:org:ws:whatever",
		"with.dns/prefix_and.Qualified-name:root:org:ws:whatever",
		"super-super-super-super-super-super-unnecessarily-long-name-for-a-thing-that-still-works:root:org:ws:whatever",
	} {
		key, value := TerminatorToLabel(testCase)
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			t.Errorf("terminator %q produces an invalid label key %q: %s", testCase, key, strings.Join(errs, ", "))
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			t.Errorf("terminator %q produces an invalid label value %q: %s", testCase, value, strings.Join(errs, ", "))
		}
	}
}

func TestTerminatorForType(t *testing.T) {
	// This test verifies the fix for https://github.com/kcp-dev/kcp/issues/4029
	// The terminator name must use the canonical path from the kcp.io/path annotation,
	// not the physical cluster name from kcp.io/cluster.
	wt := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mytype",
			Annotations: map[string]string{
				// Physical cluster name (internal identifier, like a hash)
				"kcp.io/cluster": "2cyb7pholmmc8cog",
				// Canonical path (human-readable, used in URLs and references)
				core.LogicalClusterPathAnnotationKey: "root:org:team",
			},
		},
	}

	got := TerminatorForType(wt)
	expected := corev1alpha1.LogicalClusterTerminator("root:org:team:mytype")

	if got != expected {
		t.Errorf("TerminatorForType() = %q, want %q (must use canonical path, not physical cluster)", got, expected)
	}
}
