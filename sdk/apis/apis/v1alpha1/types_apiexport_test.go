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

	apitest "github.com/kcp-dev/kcp/sdk/apis/test"
)

func TestAPIExportPermissionClaimCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old map[string]interface{}
		wantErrs     []string
	}{
		{
			name:    "nothing is set",
			current: map[string]interface{}{},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "all is true",
			current: map[string]interface{}{
				"all": true,
			},
		},
		{
			name: "all is true, resourceSelector is nil",
			current: map[string]interface{}{
				"all":              true,
				"resourceSelector": nil,
			},
		},
		{
			name: "all is true, resourceSelector is empty",
			current: map[string]interface{}{
				"all":              true,
				"resourceSelector": []interface{}{},
			},
		},
		{
			name: "all is true and resourceSelector is set",
			current: map[string]interface{}{
				"all": true,
				"resourceSelector": []interface{}{
					map[string]interface{}{"namespaces": []interface{}{"foo"}},
				},
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "all is unset and resourceSelector is nil",
			current: map[string]interface{}{
				"resourceSelector": nil,
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "all is unset and resourceSelector is empty",
			current: map[string]interface{}{
				"resourceSelector": []interface{}{},
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "resourceSelector is set",
			current: map[string]interface{}{
				"resourceSelector": []interface{}{
					map[string]interface{}{"namespaces": []interface{}{"foo"}},
				},
			},
		},
		{
			name: "all is false and resourceSelector is nil",
			current: map[string]interface{}{
				"all":              false,
				"resourceSelector": nil,
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "empty resource selector",
			current: map[string]interface{}{
				"all":              false,
				"resourceSelector": []interface{}{},
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": either \"all\" or \"resourceSelector\" must be set",
			},
		},
		{
			name: "logicalcluster invalid",
			current: map[string]interface{}{
				"group":    "core.kcp.io",
				"resource": "logicalclusters",
				"all":      true,
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": logicalclusters cannot be claimed",
			},
		},
		{
			name: "logicalcluster invalid with empty identityHash",
			current: map[string]interface{}{
				"group":        "core.kcp.io",
				"resource":     "logicalclusters",
				"identityHash": "",
				"all":          true,
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items: Invalid value: \"object\": logicalclusters cannot be claimed",
			},
		},
		{
			name: "logicalcluster fine with non-empty identityHash",
			current: map[string]interface{}{
				"group":        "core.kcp.io",
				"resource":     "logicalclusters",
				"identityHash": "abc",
				"all":          true,
			},
		},
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.io_apiexports.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items"
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

func TestResourceSelectorCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old map[string]interface{}
		wantErrs     []string
	}{
		{
			name: "none is set",
			current: map[string]interface{}{
				"names":     nil,
				"namespace": nil,
			},
			wantErrs: []string{
				"openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.resourceSelector.items: Invalid value: \"object\": at least one field must be set",
			},
		},
		{
			name: "namespaces are set",
			current: map[string]interface{}{
				"names":      nil,
				"namespaces": []interface{}{"foo"},
			},
		},
		{
			name: "names are set",
			current: map[string]interface{}{
				"names":      []interface{}{"foo"},
				"namespaces": nil,
			},
		},
		{
			name: "both name and namespaces are set",
			current: map[string]interface{}{
				"names":      []interface{}{"foo"},
				"namespaces": []interface{}{"bar"},
			},
		},
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.io_apiexports.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.resourceSelector.items"
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

func TestAPIExportPermissionClaimNamespace(t *testing.T) {
	testCases := []struct {
		name      string
		value     []interface{}
		wantError string
	}{
		{
			name:  "single valid entry",
			value: []interface{}{"ns1"},
		},
		{
			name:      "single invalid entry",
			value:     []interface{}{""},
			wantError: "\"\" is not a valid namespace. Leave field blank for cluster-scoped resources",
		},
	}

	validators := apitest.FieldValidatorsFromFile(t, "../../../../config/crds/apis.kcp.io_apiexports.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items.properties.resourceSelector.items.properties.namespaces"
		validator, found := validators["v1alpha1"][pth]
		require.True(t, found, "failed to find validator for %s", pth)

		if tc.wantError != "" {
			_ = 1 + 1
		}

		t.Run(tc.name, func(t *testing.T) {
			errs := validator(tc.value, nil)
			err := errs.ToAggregate()
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.wantError {
				t.Errorf("want error %q, got %q", tc.wantError, got)
			}
		})
	}
}
