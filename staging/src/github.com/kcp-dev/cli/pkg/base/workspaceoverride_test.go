/*
Copyright 2026 The kcp Authors.

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

package base

import (
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestResolveWorkspaceDots(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "simple name",
			path: "root",
			want: "root",
		},
		{
			name: "colon-separated path",
			path: "root:foo:bar",
			want: "root:foo:bar",
		},
		{
			name: "dot is a no-op",
			path: "root:.:foo",
			want: "root:foo",
		},
		{
			name: "leading dot",
			path: ".:root:foo",
			want: "root:foo",
		},
		{
			name: "dot-dot goes up one level",
			path: "root:foo:..",
			want: "root",
		},
		{
			name: "dot-dot in the middle",
			path: "root:foo:..bar",
			want: "root:foo:..bar",
		},
		{
			name: "multiple dot-dots",
			path: "root:foo:bar:..:..",
			want: "root",
		},
		{
			name: "dot-dot then descend",
			path: "root:foo:bar:..:baz",
			want: "root:foo:baz",
		},
		{
			name: "dot-dot from single segment yields empty path",
			path: "root:..",
			want: "",
		},
		{
			name:    "dot-dot on empty path errors",
			path:    "..",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveWorkspaceDots(tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, logicalcluster.NewPath(tt.want), got)
		})
	}
}

func TestWorkspaceOverrideClientConfig(t *testing.T) {
	tests := []struct {
		name      string
		baseHost  string
		workspace string
		wantHost  string
		wantErr   bool
	}{
		{
			name:      "absolute path replaces current workspace",
			baseHost:  "https://host/clusters/root:other",
			workspace: ":root:my-ws",
			wantHost:  "https://host/clusters/root:my-ws",
		},
		{
			name:      "relative name appended to current workspace",
			baseHost:  "https://host/clusters/root",
			workspace: "my-ws",
			wantHost:  "https://host/clusters/root:my-ws",
		},
		{
			name:      "relative name appended to nested workspace",
			baseHost:  "https://host/clusters/root:parent",
			workspace: "my-ws",
			wantHost:  "https://host/clusters/root:parent:my-ws",
		},
		{
			name:      "dot-dot navigates to parent",
			baseHost:  "https://host/clusters/root:parent:child",
			workspace: "..",
			wantHost:  "https://host/clusters/root:parent",
		},
		{
			name:      "dot-dot in absolute path",
			baseHost:  "https://host/clusters/root:other",
			workspace: ":root:parent:..",
			wantHost:  "https://host/clusters/root",
		},
		{
			name:      "base URL prefix is preserved",
			baseHost:  "https://host/prefix/clusters/root:foo",
			workspace: ":root:bar",
			wantHost:  "https://host/prefix/clusters/root:bar",
		},
		{
			name:      "host not pointing to a workspace errors",
			baseHost:  "https://host/not-a-cluster",
			workspace: "my-ws",
			wantErr:   true,
		},
		{
			name:      "dot-dot above root errors",
			baseHost:  "https://host/clusters/root",
			workspace: ":root:..",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &workspaceOverrideClientConfig{
				delegate:  &fakeClientConfig{host: tt.baseHost},
				workspace: tt.workspace,
			}
			cfg, err := c.ClientConfig()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantHost, cfg.Host)
		})
	}
}

func TestWorkspaceOverrideClientConfigDelegation(t *testing.T) {
	streams, _, _, _ := genericclioptions.NewTestIOStreams()
	o := NewOptions(streams)
	o.Workspace = ":root:ws"

	o.ClientConfig = &fakeClientConfig{host: "https://host/clusters/root"}
	require.NoError(t, o.Complete())

	_, ok := o.ClientConfig.(*workspaceOverrideClientConfig)
	require.True(t, ok, "expected ClientConfig to be wrapped in workspaceOverrideClientConfig")
}

type fakeClientConfig struct {
	host string
}

func (f *fakeClientConfig) ClientConfig() (*rest.Config, error) {
	return &rest.Config{Host: f.host}, nil
}
func (f *fakeClientConfig) RawConfig() (clientcmdapi.Config, error) { panic("not implemented") }
func (f *fakeClientConfig) Namespace() (string, bool, error)        { panic("not implemented") }
func (f *fakeClientConfig) ConfigAccess() clientcmd.ConfigAccess    { panic("not implemented") }
