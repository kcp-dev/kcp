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
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// TestAPIBindingPermissionClaimCELValidation will validate the permission claims for an otherwise valid APIBinding.
func TestAPIBindingPermissionClaimCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		claim        map[string]interface{}
		validBinding bool
	}{
		{
			name: "valid",
			claim: map[string]interface{}{
				"group":    "",
				"resource": "configmaps",
			},
			validBinding: true,
		},
		{
			name: "invalid core resource",
			claim: map[string]interface{}{
				"group":    "",
				"resource": "fakeresources",
			},
			validBinding: false,
		},
		{
			name: "invalid non core resource",
			claim: map[string]interface{}{
				"group":    "new.core.resources",
				"resource": "fakeresources",
			},
			validBinding: false,
		},
		{
			name: "valid non core resource",
			claim: map[string]interface{}{
				"group":        "new.core.resources",
				"resource":     "fakeresources",
				"identityHash": "fakehashhere",
			},
			validBinding: true,
		},
	}

	validator, s := getValidatorForPermissionClaim(t, "../../../../config/crds/apis.kcp.dev_apibindings.yaml", "acceptedPermissionClaims")

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errs, _ := validator.Validate(context.TODO(), field.NewPath("root"), &s, tc.claim, nil, cel.RuntimeCELCostBudget)
			if len(errs) == 0 && !tc.validBinding {
				t.Error("No errors were found, but should be invalid binding")
				return
			}
			if len(errs) > 0 && tc.validBinding {
				t.Errorf("found errors: %v but should be valid binding", errs.ToAggregate().Error())
				return
			}
		})
	}
}

// TestAPIBindingPermissionClaimCELValidation will validate the permission claims for an otherwise valid APIBinding.
func TestAPIExportPermissionClaimCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		claim        map[string]interface{}
		validBinding bool
	}{
		{
			name: "valid",
			claim: map[string]interface{}{
				"group":    "",
				"resource": "configmaps",
			},
			validBinding: true,
		},
		{
			name: "invalid core resource",
			claim: map[string]interface{}{
				"group":    "",
				"resource": "fakeresources",
			},
			validBinding: false,
		},
		{
			name: "invalid non core resource",
			claim: map[string]interface{}{
				"group":    "new.core.resources",
				"resource": "fakeresources",
			},
			validBinding: false,
		},
		{
			name: "valid non core resource",
			claim: map[string]interface{}{
				"group":        "new.core.resources",
				"resource":     "fakeresources",
				"identityHash": "fakehashhere",
			},
			validBinding: true,
		},
	}

	validator, s := getValidatorForPermissionClaim(t, "../../../../config/crds/apis.kcp.dev_apiexports.yaml", "permissionClaims")

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errs, _ := validator.Validate(context.TODO(), field.NewPath("root"), &s, tc.claim, nil, cel.RuntimeCELCostBudget)
			if len(errs) == 0 && !tc.validBinding {
				t.Error("No errors were found, but should be invalid binding")
				return
			}
			if len(errs) > 0 && tc.validBinding {
				t.Errorf("found errors: %v but should be valid binding", errs.ToAggregate().Error())
				return
			}
		})
	}
}

func withRule(s schema.Structural, ruleStrings []string) schema.Structural {
	rules := apiextensionsv1.ValidationRules{}

	for _, r := range ruleStrings {
		rules = append(rules, apiextensionsv1.ValidationRule{
			Rule: r,
		})
	}

	s.Extensions.XValidations = rules
	return s
}

func primitiveType(typ, format string) schema.Structural {
	result := schema.Structural{
		Generic: schema.Generic{
			Type: typ,
		},
	}
	if len(format) != 0 {
		result.ValueValidation = &schema.ValueValidation{
			Format: format,
		}
	}
	return result
}

func getValidatorForPermissionClaim(t *testing.T, crdFilePath string, specClaimName string) (*cel.Validator, schema.Structural) {

	// Get the file and marhsel to unstructed
	data, err := os.ReadFile(crdFilePath)
	require.NoError(t, err)
	u := &unstructured.Unstructured{}
	yaml.Unmarshal(data, u)

	versions, found, err := unstructured.NestedSlice(u.Object, "spec", "versions")
	require.NoError(t, err)
	require.True(t, found)
	// TODO: When adding more version, we will need to write a test to cover each version.
	// The fact that this will fail, will remind us.
	require.Len(t, versions, 1)

	version, ok := versions[0].(map[string]interface{})
	require.True(t, ok)
	validations, found, err := unstructured.NestedSlice(version, "schema", "openAPIV3Schema", "properties", "spec", "properties", specClaimName, "items", "x-kubernetes-validations")
	require.NoError(t, err)
	require.True(t, found)

	var rules []string
	for _, v := range validations {
		validationMap, ok := v.(map[string]interface{})
		require.True(t, ok)
		ruleString, ok := validationMap["rule"].(string)
		require.True(t, ok)
		rules = append(rules, ruleString)
	}

	s := schema.Structural{
		Generic: schema.Generic{
			Type: "object",
		},
		Properties: map[string]schema.Structural{
			"group":        primitiveType("string", ""),
			"resource":     primitiveType("string", ""),
			"identityHash": primitiveType("string", ""),
		},
	}

	s = withRule(s, rules)
	return cel.NewValidator(&s, cel.PerCallLimit), s
}
