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

package plugin

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestCreateContext(t *testing.T) {
	tests := []struct {
		name      string
		config    clientcmdapi.Config
		overrides *clientcmd.ConfigOverrides

		param     string
		overwrite bool

		expected   *clientcmdapi.Config
		wantStdout []string
		wantErrors []string
		wantErr    bool
	}{
		{
			name: "current, no arg",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "",
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Created context \"root:foo:bar\" and switched to it."},
		},
		{
			name: "current, with arg",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "bar",
			expected: &clientcmdapi.Config{CurrentContext: "bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"bar":                      {Cluster: "bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"bar":                      {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Created context \"bar\" and switched to it."},
		},
		{
			name: "current, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.io/current": {Server: "https://test"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "",
			wantErr: true,
		},
		{
			name: "current, existing",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "",
			wantErr: true,
		},
		{
			name: "current, existing, overwrite",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:     "",
			overwrite: true,
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Updated context \"root:foo:bar\" and switched to it."},
		},
		{
			name: "current, existing, context already set, overwrite",
			config: clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:     "",
			overwrite: true,
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Updated context \"root:foo:bar\"."},
		},
		{
			name: "current, no arg, overrides don't apply",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			overrides: &clientcmd.ConfigOverrides{
				AuthInfo: clientcmdapi.AuthInfo{
					Token: "new-token",
				},
			},
			param: "",
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"root:foo:bar":             {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":             {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Created context \"root:foo:bar\" and switched to it."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *clientcmdapi.Config

			clusterName := tt.config.Contexts[tt.config.CurrentContext].Cluster
			cluster := tt.config.Clusters[clusterName]
			u := parseURLOrDie(cluster.Server)
			u.Path = ""

			streams, _, stdout, stderr := genericclioptions.NewTestIOStreams()
			opts := NewCreateContextOptions(streams)
			opts.Name = tt.param
			opts.Overwrite = tt.overwrite
			opts.modifyConfig = func(configAccess clientcmd.ConfigAccess, config *clientcmdapi.Config) error {
				got = config
				return nil
			}
			opts.ClientConfig = clientcmd.NewDefaultClientConfig(*tt.config.DeepCopy(), nil)
			opts.startingConfig = &tt.config
			err := opts.Run(context.Background())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			t.Logf("stdout:\n%s", stdout.String())
			t.Logf("stderr:\n%s", stderr.String())

			if got != nil && tt.expected == nil {
				t.Errorf("unexpected kubeconfig write")
			} else if got == nil && tt.expected != nil {
				t.Errorf("expected a kubeconfig write, but didn't see one")
			} else if got != nil && !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("unexpected config, diff (expected, got): %s", cmp.Diff(tt.expected, got))
			}

			for _, s := range tt.wantStdout {
				require.Contains(t, stdout.String(), s)
			}
			if err != nil {
				for _, s := range tt.wantErrors {
					require.Contains(t, err.Error(), s)
				}
			}
		})
	}
}

func parseURLOrDie(host string) *url.URL {
	u, err := url.Parse(host)
	if err != nil {
		panic(err)
	}
	return u
}
