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
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpfakeclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster/fake"
)

func TestCreate(t *testing.T) {
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingWorkspaces []string // existing cluster workspaces
		markReady          bool

		newWorkspaceName                 string
		newWorkspaceType                 tenancyv1beta1.WorkspaceTypeReference
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
		{
			name: "create, use after creation, ignore existing, same absolute type",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingWorkspaces: []string{"bar"},
			newWorkspaceName:   "bar",
			newWorkspaceType:   tenancyv1beta1.WorkspaceTypeReference{Path: "root", Name: "universal"},
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
		{
			name: "reject relative type while ignoring existing",
			config: clientcmdapi.Config{CurrentContext: "test",
				Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			newWorkspaceName: "bar",
			ignoreExisting:   true,
			newWorkspaceType: tenancyv1beta1.WorkspaceTypeReference{Name: "universal"},
			wantErr:          true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			currentClusterName := logicalcluster.NewPath(strings.TrimPrefix(u.Path, "/clusters/"))
			u.Path = ""

			objects := []runtime.Object{}
			for _, name := range tt.existingWorkspaces {
				objects = append(objects, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1beta1.WorkspaceTypeReference{
							Name: "universal",
							Path: "root",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase: tenancyv1alpha1.WorkspacePhaseReady,
						URL:   fmt.Sprintf("https://test%s", currentClusterName.Join(name).RequestPath()),
					},
				})
			}
			client := kcpfakeclient.NewSimpleClientset(objects...)

			workspaceType := tt.newWorkspaceType
			empty := tenancyv1beta1.WorkspaceTypeReference{}
			if workspaceType == empty {
				workspaceType = tenancyv1beta1.WorkspaceTypeReference{
					Name: "universal",
					Path: "root",
				}
			}

			if tt.markReady {
				client.PrependReactor("create", "workspaces", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
					obj := action.(kcptesting.CreateAction).GetObject().(*tenancyv1beta1.Workspace)
					obj.Status.Phase = tenancyv1alpha1.WorkspacePhaseReady
					u := parseURLOrDie(u.String())
					u.Path = currentClusterName.Join(obj.Name).RequestPath()
					obj.Status.URL = u.String()
					obj.Spec.Type = workspaceType
					if err := client.Tracker().Cluster(currentClusterName).Create(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces"), obj, ""); err != nil {
						return false, nil, err
					}
					return true, obj, nil
				})
			}

			opts := NewCreateWorkspaceOptions(genericclioptions.NewTestIOStreamsDiscard())
			opts.Name = tt.newWorkspaceName
			opts.Type = workspaceType.Path + ":" + string(workspaceType.Name)
			opts.IgnoreExisting = tt.ignoreExisting
			opts.EnterAfterCreate = tt.useAfterCreation
			opts.ReadyWaitTimeout = time.Second
			opts.modifyConfig = func(configAccess clientcmd.ConfigAccess, config *clientcmdapi.Config) error {
				got = config
				return nil
			}
			opts.kcpClusterClient = client
			opts.ClientConfig = clientcmd.NewDefaultClientConfig(*tt.config.DeepCopy(), nil)
			err := opts.Run(context.Background())
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
	homeWorkspaceLogicalCluster := logicalcluster.NewPath("root:users:ab:cd:user-name")
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingObjects    map[logicalcluster.Name][]string
		getWorkspaceErrors map[logicalcluster.Path]error
		discovery          map[logicalcluster.Path][]*metav1.APIResourceList
		discoveryErrors    map[logicalcluster.Path]error
		unready            map[logicalcluster.Path]map[string]bool // unready workspaces
		apiBindings        []apisv1alpha1.APIBinding               // APIBindings that exist in the destination workspace, if any
		destination        string                                  // workspace set to 'current' at the end of execution
		short              bool

		param string

		expected   *clientcmdapi.Config
		wantStdout []string
		wantErrors []string
		wantErr    bool
		noWarn     bool
	}{
		{
			name: "current, context named workspace",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       ".",
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "current, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:      ".",
			wantStdout: []string{"Current workspace is the URL \"https://test\""},
		},
		{
			name: "workspace name",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, short output",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param: "bar",
			short: true,
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
			destination: "root:foo:bar",
			wantStdout:  []string{"root:foo:bar"},
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
			name: "absolute name with access to parent",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery: map[logicalcluster.Path][]*metav1.APIResourceList{
				logicalcluster.NewPath("root:foo:bar"): {&metav1.APIResourceList{
					GroupVersion: "tenancy.kcp.dev/v1alpha1",
					APIResources: []metav1.APIResource{{Name: "workspaces"}},
				}},
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
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "absolute name without access to parent",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discovery: map[logicalcluster.Path][]*metav1.APIResourceList{
				logicalcluster.NewPath("root:foo:bar"): {&metav1.APIResourceList{
					GroupVersion: "tenancy.kcp.dev/v1alpha1",
					APIResources: []metav1.APIResource{{Name: "workspaces"}},
				}},
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
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "absolute workspace doesn't exist error",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewNotFound(schema.GroupResource{}, "bar")},
			discoveryErrors: map[logicalcluster.Path]error{
				logicalcluster.NewPath("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
			},
			param:      "root:foo:foe",
			wantErr:    true,
			wantErrors: []string{"workspace \"root:foo:foe\" not found"},
		},
		{
			name: "absolute workspace access not permitted",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discoveryErrors: map[logicalcluster.Path]error{
				logicalcluster.NewPath("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
			},
			param:      "root:foo:foe",
			wantErr:    true,
			wantErrors: []string{"access to workspace root:foo:foe denied"},
		},
		{
			name: "invalid workspace name format",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:      "ju:nk§",
			wantErr:    true,
			wantErrors: []string{"invalid workspace name format: ju:nk§"},
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
			destination: "system:admin",
			wantStdout:  []string{"Current workspace is \"system:admin\""},
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
			destination: "root",
			wantStdout:  []string{"Current workspace is \"root\""},
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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root"): {"foo"},
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
			destination: "root:foo",
			wantStdout:  []string{"Current workspace is \"root:foo\""},
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
			destination: "root",
			wantStdout:  []string{"Current workspace is \"root\""},
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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is \"root:foo:bar\""},
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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
		{
			name: "~",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				tenancyv1alpha1.RootCluster: {"~"},
			},
			param: "~",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/current":  {Server: "https://test" + homeWorkspaceLogicalCluster.RequestPath()},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			destination: homeWorkspaceLogicalCluster.String(),
			wantStdout:  []string{fmt.Sprintf("Current workspace is \"%s\"", homeWorkspaceLogicalCluster.String())},
		},
		{
			name: "no arg",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				tenancyv1alpha1.RootCluster: {"~"},
			},
			param: "",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/current":  {Server: "https://test" + homeWorkspaceLogicalCluster.RequestPath()},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			destination: homeWorkspaceLogicalCluster.String(),
			wantStdout:  []string{fmt.Sprintf("Current workspace is \"%s\".\nNote: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.", homeWorkspaceLogicalCluster.String())},
		},
		{
			name: "workspace name, apibindings have matching permission and export claims",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimAccepted).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, apibindings don't have matching permission or export claims",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", "").
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "~, apibinding claims/exports don't match",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				tenancyv1alpha1.RootCluster: {"~"},
			},
			param: "~",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current":  {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"workspace.kcp.dev/previous": {Cluster: "workspace.kcp.dev/previous", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/previous": {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.dev/current":  {Server: "https://test" + homeWorkspaceLogicalCluster.RequestPath()},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			destination: homeWorkspaceLogicalCluster.String(),
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimAccepted).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				fmt.Sprintf("Current workspace is \"%s\"", homeWorkspaceLogicalCluster.String())},
		},
		{
			name: "- with existing previous context, apibinding claims/exports don't match ",
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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimAccepted).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, apibindings rejected",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimRejected).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Current workspace is \"root:foo:bar\""},
			noWarn: true,
		},
		{
			name: "workspace name, apibindings accepted",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimAccepted).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Current workspace is \"root:foo:bar\""},
			noWarn: true,
		},
		{
			name: "workspace name, some apibindings accepted",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", apisv1alpha1.ClaimAccepted).
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, apibindings not acknowledged",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", "").
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for test.test.kcp.dev:abcdef specified on APIBinding a but not accepted or rejected.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, APIBindings unacknowledged and unspecified",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", "").
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Warning: claim for test.test.kcp.dev:abcdef specified on APIBinding a but not accepted or rejected.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, multiple APIBindings unacknowledged",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.dev", "test", "abcdef", "").
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithPermissionClaim("test2.kcp.dev", "test2", "abcdef", "").
					WithExportClaim("test2.kcp.dev", "test2", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for test.test.kcp.dev:abcdef specified on APIBinding a but not accepted or rejected.\n",
				"Warning: claim for test2.test2.kcp.dev:abcdef specified on APIBinding a but not accepted or rejected.\n",
				"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "workspace name, multiple APIBindings unspecified",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
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
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha1.APIBinding{
				newBindingBuilder("a").
					WithExportClaim("test.kcp.dev", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStdout: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Warning: claim for test.test.kcp.dev:abcdef exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
				"Current workspace is \"root:foo:bar\""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			u.Path = ""

			objs := []runtime.Object{}
			for lcluster, names := range tt.existingObjects {
				for _, name := range names {
					obj := &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name:        name,
							Annotations: map[string]string{logicalcluster.AnnotationKey: lcluster.String()},
						},
						Spec: tenancyv1beta1.WorkspaceSpec{
							Type: tenancyv1beta1.WorkspaceTypeReference{
								Name: "universal",
								Path: "root",
							},
						},
					}
					if !tt.unready[lcluster.Path()][name] {
						obj.Status.Phase = tenancyv1alpha1.WorkspacePhaseReady
						obj.Status.URL = fmt.Sprintf("https://test%s", lcluster.Path().Join(name).RequestPath())
					}
					objs = append(objs, obj)
				}
			}
			client := kcpfakeclient.NewSimpleClientset(objs...)
			client.PrependReactor("get", "workspaces", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
				getAction := action.(kcptesting.GetAction)
				if getAction.GetCluster() != tenancyv1alpha1.RootCluster.Path() {
					return false, nil, nil
				}
				if getAction.GetName() == "~" {
					return true, &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "user-name",
						},
						Spec: tenancyv1beta1.WorkspaceSpec{
							Type: tenancyv1beta1.WorkspaceTypeReference{
								Name: "home",
								Path: "root",
							},
						},
						Status: tenancyv1beta1.WorkspaceStatus{
							URL: fmt.Sprintf("https://test%s", homeWorkspaceLogicalCluster.RequestPath()),
						},
					}, nil
				}
				return false, nil, nil
			})

			// return nothing in the default case.
			getAPIBindings := func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha1.APIBinding, error) {
				return nil, nil
			}

			if tt.destination != "" {
				// Add APIBindings to our Clientset if we have them
				if len(tt.apiBindings) > 0 {
					getAPIBindings = func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha1.APIBinding, error) {
						return tt.apiBindings, nil
					}
				}
			}

			client.PrependReactor("get", "workspaces", func(action kcptesting.Action) (bool, runtime.Object, error) {
				err, recorded := tt.getWorkspaceErrors[action.GetCluster()]
				return recorded, nil, err
			})

			for lcluster, d := range tt.discovery {
				if client.Resources == nil {
					client.Resources = map[logicalcluster.Path][]*metav1.APIResourceList{}
				}
				client.Resources[lcluster] = d
			}

			streams, _, stdout, stderr := genericclioptions.NewTestIOStreams()
			opts := NewUseWorkspaceOptions(streams)
			opts.Name = tt.param
			opts.ShortWorkspaceOutput = tt.short
			opts.modifyConfig = func(configAccess clientcmd.ConfigAccess, config *clientcmdapi.Config) error {
				got = config
				return nil
			}
			opts.getAPIBindings = getAPIBindings
			opts.kcpClusterClient = fakeClusterClientWithDiscoveryErrors{
				ClusterClientset: client,
				discoveryErrs:    tt.discoveryErrors,
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
			if tt.noWarn {
				require.NotContains(t, stdout.String(), "Warning")
			}
			if err != nil {
				for _, s := range tt.wantErrors {
					require.Contains(t, err.Error(), s)
				}
			}
		})
	}
}

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
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "",
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Created context \"root:foo:bar\" and switched to it."},
		},
		{
			name: "current, with arg",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param: "bar",
			expected: &clientcmdapi.Config{CurrentContext: "bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"bar":                       {Cluster: "bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"bar":                       {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Created context \"bar\" and switched to it."},
		},
		{
			name: "current, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "",
			wantErr: true,
		},
		{
			name: "current, existing",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:   "",
			wantErr: true,
		},
		{
			name: "current, existing, overwrite",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:     "",
			overwrite: true,
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Updated context \"root:foo:bar\" and switched to it."},
		},
		{
			name: "current, existing, context already set, overwrite",
			config: clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:     "",
			overwrite: true,
			expected: &clientcmdapi.Config{CurrentContext: "root:foo:bar",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{"Updated context \"root:foo:bar\"."},
		},
		{
			name: "current, no arg, overrides don't apply",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"}},
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
					"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"},
					"root:foo:bar":              {Cluster: "root:foo:bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo:bar"},
					"root:foo:bar":              {Server: "https://test/clusters/root:foo:bar"},
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

type fakeClusterClientWithDiscoveryErrors struct {
	*kcpfakeclient.ClusterClientset
	discoveryErrs map[logicalcluster.Path]error
}

func (f fakeClusterClientWithDiscoveryErrors) Cluster(cluster logicalcluster.Path) kcpclient.Interface {
	return fakeClientWithDiscoveryErrors{f.ClusterClientset.Cluster(cluster), f.discoveryErrs[cluster]}
}

type fakeClientWithDiscoveryErrors struct {
	kcpclient.Interface

	err error
}

func (c fakeClientWithDiscoveryErrors) Discovery() discovery.DiscoveryInterface {
	d := c.Interface.Discovery()
	return errorDiscoveryClient{d, c.err}
}

type errorDiscoveryClient struct {
	discovery.DiscoveryInterface
	err error
}

func (c errorDiscoveryClient) ServerGroups() (*metav1.APIGroupList, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.DiscoveryInterface.ServerGroups()
}

type bindingBuilder struct {
	apisv1alpha1.APIBinding
}

func newBindingBuilder(name string) *bindingBuilder {
	b := new(bindingBuilder)
	b.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}
	return b
}

func (b *bindingBuilder) WithPermissionClaim(group, resource, identityHash string, state apisv1alpha1.AcceptablePermissionClaimState) *bindingBuilder {
	if len(b.Spec.PermissionClaims) == 0 {
		b.Spec.PermissionClaims = make([]apisv1alpha1.AcceptablePermissionClaim, 0)
	}

	pc := apisv1alpha1.AcceptablePermissionClaim{
		PermissionClaim: apisv1alpha1.PermissionClaim{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    group,
				Resource: resource,
			},
			IdentityHash: identityHash,
		},
	}

	if state != "" {
		pc.State = state
	}

	b.Spec.PermissionClaims = append(b.Spec.PermissionClaims, pc)
	return b
}

func (b *bindingBuilder) WithExportClaim(group, resource, identityHash string) *bindingBuilder {
	if len(b.Status.ExportPermissionClaims) == 0 {
		b.Status.ExportPermissionClaims = make([]apisv1alpha1.PermissionClaim, 0)
	}

	pc := apisv1alpha1.PermissionClaim{
		GroupResource: apisv1alpha1.GroupResource{
			Group:    group,
			Resource: resource,
		},
		IdentityHash: identityHash,
	}

	b.Status.ExportPermissionClaims = append(b.Status.ExportPermissionClaims, pc)
	return b
}

func (b *bindingBuilder) Build() apisv1alpha1.APIBinding {
	return b.APIBinding
}
