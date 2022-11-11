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

func TestInitializerToLabel(t *testing.T) {
	for _, testCase := range []tenancyv1alpha1.WorkspaceInitializer{
		"simple:root:org:ws:whatever",
		"QualifiedName:root:org:ws:whatever",
		"qualified.Name:root:org:ws:whatever",
		"qualified-123-name:root:org:ws:whatever",
		"with.dns/prefix:root:org:ws:whatever",
		"with.dns/prefix_and.Qualified-name:root:org:ws:whatever",
		"super-super-super-super-super-super-unnecessarily-long-name-for-a-thing-that-still-works:root:org:ws:whatever",
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
