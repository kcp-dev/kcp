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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	apitest "github.com/kcp-dev/kcp/pkg/apis/test"
)

// TestAPIBindingPermissionClaimCELValidation will validate the permission claims for an otherwise valid APIBinding.
func TestAPIBindingPermissionClaimCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old map[string]interface{}
		wantErrs     []string
	}{
		{
			name: "no change",
			current: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "foo",
					"name": "bar",
				},
			},
		},
		{
			name: "change exportName",
			current: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "foo",
					"name": "CHANGE",
				},
			},
			wantErrs: []string{"openAPIV3Schema.properties.spec.properties.reference: Invalid value: \"object\": APIExport reference must not be changed"},
		},
		{
			name: "change path",
			current: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]interface{}{
				"export": map[string]interface{}{
					"path": "CHANGE",
					"name": "bar",
				},
			},
			wantErrs: []string{"openAPIV3Schema.properties.spec.properties.reference: Invalid value: \"object\": APIExport reference must not be changed"},
		},
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.dev_apibindings.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.reference"
		validator, found := validators["v1alpha1"][pth]
		require.True(t, found, "failed to find validator for %s", pth)

		t.Run(tc.name, func(t *testing.T) {
			errs := validator(tc.current, tc.old)
			t.Log(errs)

			if got := len(errs); got != len(tc.wantErrs) {
				t.Errorf("expected errors %v, got %v", len(tc.wantErrs), len(errs))
				return
			}

			for i := range tc.wantErrs {
				got := errs[i].Error()
				if got != tc.wantErrs[i] {
					t.Errorf("want error %q, got %q", tc.wantErrs[i], got)
				}
			}
		})
	}
}
