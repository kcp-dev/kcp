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

package initialization

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestClusterWorkspaceInitializerLabelPrefix(t *testing.T) {
	// we want to be able to add this prefix to any valid initializer and have it
	// end up as a valid label, so it needs to be a DNS 1123 subdomain
	if errs := validation.IsDNS1123Subdomain(tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix + "a"); len(errs) > 0 {
		t.Errorf("tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix invalid: %s", strings.Join(errs, ", "))
	}
}

func TestInitializerLabelFor(t *testing.T) {
	for _, testCase := range []tenancyv1alpha1.ClusterWorkspaceInitializer{
		{Name: "simple", Path: "root:org:ws:whatever"},
		{Name: "QualifiedName", Path: "root:org:ws:whatever"},
		{Name: "qualified.Name", Path: "root:org:ws:whatever"},
		{Name: "qualified-123-name", Path: "root:org:ws:whatever"},
		{Name: "with.dns/prefix", Path: "root:org:ws:whatever"},
		{Name: "with.dns/prefix_and.Qualified-name", Path: "root:org:ws:whatever"},
		{Name: "super-super-super-super-super-super-unnecessarily-long-name-for-a-thing-that-still-works", Path: "root:org:ws:whatever"},
	} {
		key, value := InitializerToLabel(testCase)
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			t.Errorf("initializer %q produces an invalid label key %q: %s", testCase, key, strings.Join(errs, ", "))
		}
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			t.Errorf("initializer %q produces an invalid label value %q: %s", testCase, value, strings.Join(errs, ", "))
		}
	}
}
