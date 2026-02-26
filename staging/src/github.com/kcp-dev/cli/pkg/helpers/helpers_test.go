/*
Copyright 2022 The kcp Authors.

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

package helpers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestParseClusterURL(t *testing.T) {
	tests := []struct {
		host    string
		url     string
		cluster string
		wantErr bool
	}{
		{host: "", wantErr: true},
		{host: "garbgae", wantErr: true},
		{host: "https://host/foo", wantErr: true},
		{host: "https://host/clusters/root", url: "https://host", cluster: "root"},
		{host: "https://host/clusters/root:foo", url: "https://host", cluster: "root:foo"},
		{host: "https://host/clusters/system:foo", url: "https://host", cluster: "system:foo"},
		{host: "https://host/clusters/abc:def%", wantErr: true},
		{host: "https://host/clusters/", wantErr: true},
		{host: "https://host/clusters", wantErr: true},
		{host: "https://host/clusters/root:foo:bar", url: "https://host", cluster: "root:foo:bar"},
		{host: "https://host/clusters/root:foo/abc", url: "https://host", cluster: "root:foo"},
		{host: "https://host/services/workspaces/root:foo:bar", wantErr: true},
		{host: "https://host/services/workspaces/root:foo:bar/abc", wantErr: true},
		{host: "https://host/services/workspaces/", wantErr: true},
		{host: "https://host/services/workspaces", wantErr: true},
		{host: "https://host/abc/clusters/root:foo", url: "https://host/abc", cluster: "root:foo"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			gotURL, gotCluster, err := ParseClusterURL(tt.host)
			if tt.wantErr {
				require.Error(t, err, "instead of error got %q, %q", gotURL, gotCluster)
			} else {
				require.NoError(t, err)
			}
			var gotURLStr string
			if gotURL != nil {
				gotURLStr = gotURL.String()
			}
			if gotURLStr != tt.url {
				t.Errorf("url, _ := parseClusterURL(%q) got = %v, want %v", tt.host, gotURL, tt.url)
			}
			if gotCluster != logicalcluster.NewPath(tt.cluster) {
				t.Errorf("_, cluster := parseClusterURL(%q) got = %v, want %v", tt.host, gotCluster, tt.cluster)
			}
		})
	}
}
