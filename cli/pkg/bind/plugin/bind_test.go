/*
Copyright 2023 The KCP Authors.

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
		wantError   bool
	}{
		{
			description: "Valid permission claim with all fields",
			claim:       "group=core:resource=secrets:all=true:state=Accepted",
			wantError:   false,
		},
		{
			description: "Valid permission claim with minimal fields",
			claim:       "resource=secrets:state=Accepted",
			wantError:   false,
		},
		{
			description: "Invalid permission claim with missing value",
			claim:       "resource=secrets:state=",
			wantError:   true,
		},
		{
			description: "Invalid permission claim with invalid state",
			claim:       "resource=secrets:state=InvalidState",
			wantError:   true,
		},
		{
			description: "Invalid permission claim with missing key",
			claim:       "resource=secrets:=Accepted",
			wantError:   true,
		},
		{
			description: "Invalid permission claim with extra colon",
			claim:       "resource=secrets:state=Accepted:",
			wantError:   true,
		},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			bindOptions := BindOptions{}
			err := bindOptions.parsePermissionClaim(c.claim)
			if (err != nil) && !c.wantError {
				t.Errorf("wanted %q to be valid, but got error: %v", c.claim, err)
			}

			if (err == nil) && c.wantError {
				t.Errorf("wanted %q to be invalid, but got no error", c.claim)
			}
		})
	}
}
