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
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	clientgotesting "k8s.io/client-go/testing"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyfake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
)

func TestCreate(t *testing.T) {
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingWorkspaces []string // existing cluster workspaces
		markReady          bool

		newWorkspaceName                 string
		newWorkspaceType                 string
		useAfterCreation, ignoreExisting bool

		expected *clientcmdapi.Config
		wantErr  bool
	}{
		{
			name: "happy case, no use",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			newWorkspaceName: "bar",
		},
		{
			name: "create, use after creation, but not ready",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			newWorkspaceName: "bar",
			useAfterCreation: true,
			wantErr:          true, // not ready
		},
		{
			name: "create, use after creation",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			newWorkspaceName: "bar",
			useAfterCreation: true,
			markReady:        true,
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"test":                       {Cluster: "test", AuthInfo: "test"},
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test":                      {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
		},
		{
			name: "create, already existing",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingWorkspaces: []string{"bar"},
			newWorkspaceName:   "bar",
			useAfterCreation:   true,
			wantErr:            true,
		},
		{
			name: "create, use after creation, ignore existing",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingWorkspaces: []string{"bar"},
			newWorkspaceName:   "bar",
			useAfterCreation:   true,
			markReady:          true,
			ignoreExisting:     true,
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"test":                       {Cluster: "test", AuthInfo: "test"},
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test":                      {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			currentClusterName := logicalcluster.New(strings.TrimPrefix(u.Path, "/clusters/"))
			u.Path = ""

			objects := []runtime.Object{}
			for _, name := range tt.existingWorkspaces {
				objects = append(objects, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: "Universal",
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
						URL:   fmt.Sprintf("https://test%s", currentClusterName.Join(name).Path()),
					},
				})
			}
			client := tenancyfake.NewSimpleClientset(objects...)

			if tt.markReady {
				client.PrependReactor("create", "workspaces", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
					obj := action.(clientgotesting.CreateAction).GetObject().(*tenancyv1beta1.Workspace)
					obj.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
					u := parseURLOrDie(u.String())
					u.Path = currentClusterName.Join(obj.Name).Path()
					obj.Status.URL = u.String()
					obj.Spec.Type = tt.newWorkspaceType
					if obj.Spec.Type == "" {
						obj.Spec.Type = "Universal"
					}
					if err := client.Tracker().Create(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces"), obj, ""); err != nil {
						return false, nil, err
					}
					return true, obj, nil
				})
			}

			kc := &KubeConfig{
				startingConfig: tt.config.DeepCopy(),
				currentContext: tt.config.CurrentContext,

				clusterClient: fakeTenancyClient{
					t: t,
					clients: map[logicalcluster.LogicalCluster]*tenancyfake.Clientset{
						currentClusterName: client,
					},
				},
				personalClient: fakeTenancyClient{
					t: t,
					clients: map[logicalcluster.LogicalCluster]*tenancyfake.Clientset{
						currentClusterName: client,
					},
				},
				modifyConfig: func(config *clientcmdapi.Config) error {
					got = config
					return nil
				},
				IOStreams: genericclioptions.NewTestIOStreamsDiscard(),
			}
			err := kc.CreateWorkspace(context.TODO(), tt.newWorkspaceName, tt.newWorkspaceType, tt.ignoreExisting, tt.useAfterCreation, time.Second)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			if got != nil && tt.expected == nil {
				t.Errorf("unexpected kubeconfig write")
			} else if got == nil && tt.expected != nil {
				t.Errorf("expected a kubeconfig write, but didn't see one")
			} else if got != nil && !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("unexpected config, diff (expected, got): %s", cmp.Diff(tt.expected, got))
			}
		})
	}
}

func TestUse(t *testing.T) {
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingObjects map[logicalcluster.LogicalCluster][]string
		prettyNames     map[logicalcluster.LogicalCluster]map[string]string
		unready         map[logicalcluster.LogicalCluster]map[string]bool // unready workspaces

		param string

		expected   *clientcmdapi.Config
		wantStdout []string
		wantErrors []string
		wantErr    bool
	}{
		{
			name: "current, context named workspace",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param:      "",
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "current, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:      "",
			wantStdout: []string{"Current workspace is the URL \"https://test\""},
		},
		{
			name: "workspace name",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param: "bar",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo:bar"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace pretty name",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param: "baz",
			prettyNames: map[logicalcluster.LogicalCluster]map[string]string{
				logicalcluster.New("root:foo"): {"bar": "baz"},
			},
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo:bar"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "bar",
			wantErr: true,
		},
		{
			name: "absolute name",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			prettyNames: map[logicalcluster.LogicalCluster]map[string]string{
				logicalcluster.New("root:foo"): {"bar": "baz"},
			},
			param: "root:foo:bar",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo:bar"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "absolute name, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "root:bar",
			wantErr: true,
		},
		{
			name: "system:admin",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "system:admin",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/system:admin"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"system:admin\""},
		},
		{
			name: "root",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "root",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root\""},
		},
		{
			name: "root, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "root",
			wantErr: true,
		},
		{
			name: "..",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root"): {"foo"},
			},
			param: "..",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root:foo\""},
		},
		{
			name: ".., no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "..",
			wantErr: true,
		},
		{
			name: ".. to root",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "..",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Current workspace is \"root\""},
		},
		{
			name: ".. in root",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:    "..",
			expected: nil,
			wantErr:  true,
		},
		{
			name: "- with existing previous context",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test2"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"test2": {Token: "test2"},
				},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param: "-",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test2"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo:bar"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"test2": {Token: "test2"},
				},
			},
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "- without existing previous context",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param:   "-",
			wantErr: true,
		},
		{
			name: "- with non-cluster context",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "other", AuthInfo: "other"},
					"other":                      {Cluster: "other", AuthInfo: "other"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"},
					"other":                     {Server: "https://other/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"other": {Token: "test"},
				},
			},
			existingObjects: map[logicalcluster.LogicalCluster][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param: "-",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "other", AuthInfo: "other"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
					"other":                      {Cluster: "other", AuthInfo: "other"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current":  {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
					"other":                      {Server: "https://other/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"other": {Token: "test"},
				},
			},
			wantStdout: []string{"Current workspace is the URL \"https://other/\""},
		},
		{
			name: "- with non-cluster context, with non-cluster previous context",
			config: clientcmdapi.Config{CurrentContext: "other",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "other2", AuthInfo: "other2"},
					"other":                      {Cluster: "other", AuthInfo: "other"},
					"other2":                     {Cluster: "other2", AuthInfo: "other2"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"},
					"other":                     {Server: "https://other/"},
					"other2":                    {Server: "https://other2/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":   {Token: "test"},
					"other":  {Token: "test"},
					"other2": {Token: "test2"},
				},
			},
			param: "-",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "other2", AuthInfo: "other2"},
					"workspace.kcp.dev/previous": {Cluster: "other", AuthInfo: "other"},
					"other":                      {Cluster: "other", AuthInfo: "other"},
					"other2":                     {Cluster: "other2", AuthInfo: "other2"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"},
					"other":                     {Server: "https://other/"},
					"other2":                    {Server: "https://other2/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":   {Token: "test"},
					"other":  {Token: "test"},
					"other2": {Token: "test2"},
				},
			},
			wantStdout: []string{"Current workspace is the URL \"https://other2/\""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			u.Path = ""

			clients := map[logicalcluster.LogicalCluster]*tenancyfake.Clientset{}
			personalClients := map[logicalcluster.LogicalCluster]*tenancyfake.Clientset{}
			for lcluster, names := range tt.existingObjects {
				objs := []runtime.Object{}
				prettyObjs := []runtime.Object{}
				for _, name := range names {
					obj := &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name: name,
						},
						Spec: tenancyv1beta1.WorkspaceSpec{
							Type: "Universal",
						},
					}
					if !tt.unready[lcluster][name] {
						obj.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
						obj.Status.URL = fmt.Sprintf("https://test%s", lcluster.Join(name).Path())
					}
					objs = append(objs, obj)

					// pretty name?
					if pretty, ok := tt.prettyNames[lcluster][name]; ok {
						obj = obj.DeepCopy()
						obj.Name = pretty
					}
					prettyObjs = append(prettyObjs, obj)
				}
				clients[lcluster] = tenancyfake.NewSimpleClientset(objs...)
				personalClients[lcluster] = tenancyfake.NewSimpleClientset(prettyObjs...)
			}

			streams, _, stdout, stderr := genericclioptions.NewTestIOStreams()

			kc := &KubeConfig{
				startingConfig: tt.config.DeepCopy(),
				currentContext: tt.config.CurrentContext,

				clusterClient: fakeTenancyClient{
					t:       t,
					clients: clients,
				},
				personalClient: fakeTenancyClient{
					t:       t,
					clients: personalClients,
				},
				modifyConfig: func(config *clientcmdapi.Config) error {
					got = config
					return nil
				},
				IOStreams: streams,
			}
			err := kc.UseWorkspace(context.TODO(), tt.param)
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

type fakeTenancyClient struct {
	t       *testing.T
	clients map[logicalcluster.LogicalCluster]*tenancyfake.Clientset
}

func (f fakeTenancyClient) Cluster(cluster logicalcluster.LogicalCluster) tenancyclient.Interface {
	client, ok := f.clients[cluster]
	require.True(f.t, ok, "no client for cluster %s", cluster)
	return client
}
