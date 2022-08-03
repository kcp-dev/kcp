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
func TestAPIExportPermissionClaimCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		claim, old   map[string]interface{}
		validBinding bool
	}{
		{
			name: "valid empty group",
			claim: map[string]interface{}{
				"group":    "",
				"resource": "configmaps",
			},
			validBinding: true,
		},
		{
			name: "valid k8s.io resource",
			claim: map[string]interface{}{
				"group":    "fake.k8s.io",
				"resource": "fakeresources",
			},
			validBinding: true,
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

	validators := apitest.ValidatorsFromFile(t, "../../../../config/crds/apis.kcp.dev_apiexports.yaml")

	for _, tc := range testCases {
		pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items"
		validator, found := validators["v1alpha1"][pth]
		require.True(t, found, "failed to find validator for %s", pth)

		t.Run(tc.name, func(t *testing.T) {
			errs := validator(tc.claim, tc.old)
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
