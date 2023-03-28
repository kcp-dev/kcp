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

	"sigs.k8s.io/yaml"

	apitest "github.com/kcp-dev/kcp/sdk/apis/test"
)

func TestWorkspaceCELValidation(t *testing.T) {
	testCases := []struct {
		name         string
		current, old string
		wantErrs     []string
	}{
		{
			name:    "nothing is set",
			current: "{}",
		},
		{
			name:    "unset URL",
			old:     `{"spec":{"URL": "abc"}}`,
			current: `{"spec":{}}`,
			wantErrs: []string{
				"spec: Invalid value: \"object\": URL cannot be unset",
			},
		},
		{
			name:    "unset cluster",
			old:     `{"spec":{"cluster": "abc"}}`,
			current: `{"spec":{}}`,
			wantErrs: []string{
				"spec: Invalid value: \"object\": cluster cannot be unset",
			},
		},
		{
			name:    "change cluster",
			old:     `{"spec":{"cluster": "abc"}}`,
			current: `{"spec":{"cluster": "def"}}`,
			wantErrs: []string{
				"spec.cluster: Invalid value: \"string\": cluster is immutable",
			},
		},
		{
			name:    "unchanged cluster",
			old:     `{"status":{"cluster": "def"}}`,
			current: `{"status":{"cluster": "def"}}`,
		},
	}

	validator, err := apitest.VersionValidatorFromFile(t, "../../../../config/crds/tenancy.kcp.io_workspaces.yaml", "v1alpha1")
	require.NoError(t, err)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var current interface{}
			err := yaml.Unmarshal([]byte(tc.current), &current)
			require.NoError(t, err)

			var old interface{}
			if tc.old != "" {
				err = yaml.Unmarshal([]byte(tc.old), &old)
				require.NoError(t, err)
			}

			errs := validator(current, old)
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
