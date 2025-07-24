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

	"github.com/stretchr/testify/require"

	apitest "github.com/kcp-dev/kcp/sdk/apis/test"
)

// TestAPIBindingAPIExportReferenceCELValidation will validate the APIExport reference for an otherwise valid APIBinding.
func TestAPIBindingAPIExportReferenceCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old map[string]any
		wantErrs     []string
	}{
		{
			name: "no change",
			current: map[string]any{
				"export": map[string]any{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]any{
				"export": map[string]any{
					"path": "foo",
					"name": "bar",
				},
			},
		},
		{
			name: "change export name",
			current: map[string]any{
				"export": map[string]any{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]any{
				"export": map[string]any{
					"path": "foo",
					"name": "CHANGE",
				},
			},
			wantErrs: []string{"openAPIV3Schema.properties.spec.properties.reference: Invalid value: \"object\": APIExport reference must not be changed"},
		},
		{
			name: "change path",
			current: map[string]any{
				"export": map[string]any{
					"path": "foo",
					"name": "bar",
				},
			},
			old: map[string]any{
				"export": map[string]any{
					"path": "CHANGE",
					"name": "bar",
				},
			},
			wantErrs: []string{"openAPIV3Schema.properties.spec.properties.reference: Invalid value: \"object\": APIExport reference must not be changed"},
		},
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.io_apibindings.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.reference"
		validator, found := validators["v1alpha2"][pth]
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

// TestAPIBindingAPIExportReferenceCELValidation will validate the APIExport reference for an otherwise valid APIBinding.
func TestAPIBindingPermissionClaimSelectorCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old map[string]any
		wantErrs     []string
	}{
		{
			name: "no change",
			current: map[string]any{
				"matchAll": true,
			},
			old: map[string]any{
				"matchAll": true,
			},
		},
		{
			name:    "nothing set",
			current: map[string]any{},
			old:     map[string]any{},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.selector: Invalid value: \"object\": a selector is required. Only \"matchAll\" is currently implemented",
			},
		},
		{
			name: "using matchLabels",
			current: map[string]any{
				"matchLabels": map[string]any{
					"kcp.io/fake": "test",
				},
			},
			old: map[string]any{
				"matchLabels": map[string]any{
					"kcp.io/fake": "test",
				},
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.selector: Invalid value: \"object\": a selector is required. Only \"matchAll\" is currently implemented",
			},
		},
		/*
			// TODO(embik): for future updates to unlock other matchers
				{
					name: "change from matchAll to matchLabels",
					current: map[string]any{
						"matchLabels": map[string]any{
							"kcp.io/fake": "test",
						},
					},
					old: map[string]any{
						"matchAll": true,
					},
					wantErrs: []string{
						"openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.selector: Invalid value: \"object\": Permission claim selector must not be changed",
					},
				},
				{
					name: "multiple matchers configured",
					current: map[string]any{
						"matchLabels": map[string]any{
							"kcp.io/fake": "test",
						},
						"matchAll": true,
					},
					old: map[string]any{
						"matchLabels": map[string]any{
							"kcp.io/fake": "test",
						},
						"matchAll": true,
					},
					wantErrs: []string{
						"openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.selector: Invalid value: \"object\": either \"matchAll\", \"matchLabels\" or \"matchExpressions\" must be set",
					},
				},
		*/
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.io_apibindings.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.selector"
		validator, found := validators["v1alpha2"][pth]
		require.True(t, found, "failed to find validator for %s", pth)

		t.Run(tc.name, func(t *testing.T) {
			errs := validator(tc.current, tc.old)
			if len(errs) > 0 {
				for _, err := range errs {
					t.Logf("'%s': observed validation error: %s", tc.name, err)
				}
			}

			if got := len(errs); got != len(tc.wantErrs) {
				t.Fatalf("'%s': expected %d errors, got %d", tc.name, len(tc.wantErrs), len(errs))
			}

			for i := range tc.wantErrs {
				got := errs[i].Error()
				if got != tc.wantErrs[i] {
					t.Errorf("'%s': want error %q, got %q", tc.name, tc.wantErrs[i], got)
				}
			}
		})
	}
}
