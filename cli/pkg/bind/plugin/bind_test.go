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

package plugin_test

import (
	"testing"

	"github.com/kcp-dev/kcp/cli/pkg/bind/plugin"
)

func TestBindOptionsValidate(t *testing.T) {
	testCases := []struct {
		description string
		bindOptions plugin.BindOptions
		wantValid   bool
	}{
		{
			description: "Fully-qualified APIExport reference with dashes",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:test-workspace:test-apiexport-123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with dots in the name",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:test-workspace:test.apiexport.123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the name",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:test-workspace:test@apiexport=123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the name",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:test-workspace:testAPIExport123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the path",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:t@st-workspace:test-apiexport"},
			wantValid:   false,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the path",
			bindOptions: plugin.BindOptions{APIExportRef: "test-root:TestWorkspace:test-apiexport"},
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
