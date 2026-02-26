/*
Copyright 2023 The kcp Authors.

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

package plugin

import (
	"testing"
)

func TestBindOptionsValidate(t *testing.T) {
	testCases := []struct {
		description string
		bindOptions BindOptions
		wantValid   bool
	}{
		{
			description: "Fully-qualified APIExport reference with dashes",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test-apiexport-123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with dots in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test.apiexport.123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test@apiexport=123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:testAPIExport123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the path",
			bindOptions: BindOptions{APIExportRef: "test-root:t@st-workspace:test-apiexport"},
			wantValid:   false,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the path",
			bindOptions: BindOptions{APIExportRef: "test-root:TestWorkspace:test-apiexport"},
			wantValid:   false,
		},
		{
			description: "Valid accepted permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"group.resource"},
			},
			wantValid: true,
		},
		{
			description: "Valid rejected permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				RejectedPermissionClaims: []string{"resource.group"},
			},
			wantValid: true,
		},
		{
			description: "Conflicting accepted and rejected permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"resource.group"},
				RejectedPermissionClaims: []string{"resource.group"},
			},
			wantValid: false,
		},
		{
			description: "Invalid accepted permission claim format",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"invalidclaim"},
			},
			wantValid: false,
		},
		{
			description: "Invalid rejected permission claim format",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				RejectedPermissionClaims: []string{"invalidclaim"},
			},
			wantValid: false,
		},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			err := c.bindOptions.Validate()

			if (err != nil) && c.wantValid {
				t.Errorf("wanted %+v to be valid, but it's invalid", c.bindOptions)
			}

			if (err == nil) && !c.wantValid {
				t.Errorf("wanted %+v to be invalid, but it's valid", c.bindOptions)
			}
		})
	}
}

func TestParsePermissionClaim(t *testing.T) {
	testCases := []struct {
		description string
		claim       string
		accepted    bool
		wantError   bool
	}{
		{
			description: "Valid accepted permission claim",
			claim:       "resource.group",
			accepted:    true,
			wantError:   false,
		},
		{
			description: "Valid rejected permission claim",
			claim:       "resource.group",
			accepted:    false,
			wantError:   false,
		},
		{
			description: "Invalid permission claim format",
			claim:       "invalidclaim",
			accepted:    true,
			wantError:   true,
		},
		{
			description: "Empty permission claim",
			claim:       "",
			accepted:    true,
			wantError:   true,
		},
		{
			description: "Core group permission claim",
			claim:       "resource.core",
			accepted:    true,
			wantError:   false,
		},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			b := &BindOptions{}
			err := b.parsePermissionClaim(c.claim, c.accepted)

			if (err != nil) && !c.wantError {
				t.Errorf("wanted %q to be parsed without error, but got error: %v", c.claim, err)
			}

			if (err == nil) && c.wantError {
				t.Errorf("wanted %q to be parsed with error, but got no error", c.claim)
			}
		})
	}
}
