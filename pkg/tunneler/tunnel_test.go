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

package tunneler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncerTunnelURL(t *testing.T) {
	tests := map[string]struct {
		host        string
		workspace   string
		target      string
		expected    string
		expectError bool
	}{
		"valid host, no ports": {
			host:      "https://example.com",
			workspace: "root:testing:testing",
			target:    "cluster1",
			expected:  "https://example.com/services/syncer-tunnels/clusters/root:testing:testing/apis/workload.kcp.dev/v1alpha1/synctargets/cluster1",
		},
		"valid host, with ports": {
			host:      "https://example.com:443",
			workspace: "root:testing:testing",
			target:    "cluster1",
			expected:  "https://example.com:443/services/syncer-tunnels/clusters/root:testing:testing/apis/workload.kcp.dev/v1alpha1/synctargets/cluster1",
		},
		"invalid host": {
			host:        "example.com:443:443",
			workspace:   "root:testing:testing",
			target:      "cluster1",
			expectError: true,
		},
		"invalid host, no scheme": {
			host:        "example.com:443:443",
			workspace:   "root:testing:testing",
			target:      "cluster1",
			expectError: true,
		},
		"invalid host, no scheme, no port": {
			host:        "example.com:443:443",
			workspace:   "root:testing:testing",
			target:      "cluster1",
			expectError: true,
		},
		"invalid host, no scheme, no port, no host": {
			host:        ":443:443",
			workspace:   "root:testing:testing",
			target:      "cluster1",
			expectError: true,
		},
		"empty host": {
			host:        "",
			workspace:   "root:testing:testing",
			target:      "cluster1",
			expectError: true,
		},
		"empty workspace": {
			host:        "example.com:443",
			workspace:   "",
			target:      "cluster1",
			expectError: true,
		},
		"empty target": {
			host:        "example.com:443",
			workspace:   "root:testing:testing",
			target:      "",
			expectError: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := SyncerTunnelURL(tc.host, tc.workspace, tc.target)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expected, got)
			}
		})
	}
}
