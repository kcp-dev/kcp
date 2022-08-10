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
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
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
		newWorkspaceType                 tenancyv1alpha1.ClusterWorkspaceTypeReference
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
			newWorkspaceType:   tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "universal"},
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
			newWorkspaceType: tenancyv1alpha1.ClusterWorkspaceTypeReference{Name: "universal"},
			wantErr:          true,
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
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "universal",
							Path: "root",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
						URL:   fmt.Sprintf("https://test%s", currentClusterName.Join(name).Path()),
					},
				})
			}
			client := tenancyfake.NewSimpleClientset(objects...)

			workspaceType := tt.newWorkspaceType
			empty := tenancyv1alpha1.ClusterWorkspaceTypeReference{}
			if workspaceType == empty {
				workspaceType = tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Name: "universal",
					Path: "root",
				}
			}

			if tt.markReady {
				client.PrependReactor("create", "workspaces", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
					obj := action.(clientgotesting.CreateAction).GetObject().(*tenancyv1beta1.Workspace)
					obj.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
					u := parseURLOrDie(u.String())
					u.Path = currentClusterName.Join(obj.Name).Path()
					obj.Status.URL = u.String()
					obj.Spec.Type = workspaceType
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
					clients: map[logicalcluster.Name]*tenancyfake.Clientset{
						currentClusterName: client,
					},
				},
				modifyConfig: func(config *clientcmdapi.Config) error {
					got = config
					return nil
				},
				IOStreams: genericclioptions.NewTestIOStreamsDiscard(),
			}
			err := kc.CreateWorkspace(context.Background(), tt.newWorkspaceName, workspaceType.Path+":"+string(workspaceType.Name), tt.ignoreExisting, tt.useAfterCreation, time.Second)
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
	homeWorkspaceLogicalCluster := logicalcluster.New("root:users:ab:cd:user-name")
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingObjects    map[logicalcluster.Name][]string
		getWorkspaceErrors map[logicalcluster.Name]error
		discovery          map[logicalcluster.Name][]*metav1.APIResourceList
		discoveryErrors    map[logicalcluster.Name]error
		unready            map[logicalcluster.Name]map[string]bool // unready workspaces
		short              bool

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
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.New("root:foo"): {"bar"},
			},
			param:      ".",
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
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
			name: "workspace name, short output",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingObjects: map[logicalcluster.Name][]string{
				logicalcluster.New("root:foo"): {"bar"},
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
			wantStdout: []string{"root:foo:bar"},
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
				logicalcluster.New("root:foo"): {"bar"},
			},
			discovery: map[logicalcluster.Name][]*metav1.APIResourceList{
				logicalcluster.New("root:foo:bar"): {&metav1.APIResourceList{
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
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "absolute name without access to parent",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			getWorkspaceErrors: map[logicalcluster.Name]error{logicalcluster.New("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discovery: map[logicalcluster.Name][]*metav1.APIResourceList{
				logicalcluster.New("root:foo:bar"): {&metav1.APIResourceList{
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
			wantStdout: []string{"Current workspace is \"root:foo:bar\""},
		},
		{
			name: "absolute workspace doesn't exist error",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.dev/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.dev/current": {Cluster: "workspace.kcp.dev/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.dev/current": {Server: "https://test/clusters/root:foo"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			getWorkspaceErrors: map[logicalcluster.Name]error{logicalcluster.New("root:foo"): errors.NewNotFound(schema.GroupResource{}, "bar")},
			discoveryErrors: map[logicalcluster.Name]error{
				logicalcluster.New("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
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
			getWorkspaceErrors: map[logicalcluster.Name]error{logicalcluster.New("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discoveryErrors: map[logicalcluster.Name]error{
				logicalcluster.New("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
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
				logicalcluster.New("root:foo"): {"bar"},
			},
			param:      "ju:nk",
			wantErr:    true,
			wantErrors: []string{"invalid workspace name format: ju:nk"},
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
			existingObjects: map[logicalcluster.Name][]string{
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
			existingObjects: map[logicalcluster.Name][]string{
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
			existingObjects: map[logicalcluster.Name][]string{
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
			existingObjects: map[logicalcluster.Name][]string{
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
					"workspace.kcp.dev/current":  {Server: "https://test" + homeWorkspaceLogicalCluster.Path()},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{fmt.Sprintf("Current workspace is \"%s\"", homeWorkspaceLogicalCluster.String())},
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
					"workspace.kcp.dev/current":  {Server: "https://test" + homeWorkspaceLogicalCluster.Path()},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			wantStdout: []string{fmt.Sprintf("Current workspace is \"%s\".\nNote: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.", homeWorkspaceLogicalCluster.String())},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			u.Path = ""

			clients := map[logicalcluster.Name]*tenancyfake.Clientset{}
			for lcluster, names := range tt.existingObjects {
				objs := []runtime.Object{}
				for _, name := range names {
					obj := &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name: name,
						},
						Spec: tenancyv1beta1.WorkspaceSpec{
							Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
								Name: "universal",
								Path: "root",
							},
						},
					}
					if !tt.unready[lcluster][name] {
						obj.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
						obj.Status.URL = fmt.Sprintf("https://test%s", lcluster.Join(name).Path())
					}
					objs = append(objs, obj)
				}
				clients[lcluster] = tenancyfake.NewSimpleClientset(objs...)

				if lcluster == tenancyv1alpha1.RootCluster {
					clients[lcluster].PrependReactor("get", "workspaces", func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
						getAction := action.(clientgotesting.GetAction)
						if getAction.GetName() == "~" {
							return true, &tenancyv1beta1.Workspace{
								ObjectMeta: metav1.ObjectMeta{
									Name: "user-name",
								},
								Spec: tenancyv1beta1.WorkspaceSpec{
									Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
										Name: "home",
										Path: "root",
									},
								},
								Status: tenancyv1beta1.WorkspaceStatus{
									URL: fmt.Sprintf("https://test%s", homeWorkspaceLogicalCluster.Path()),
								},
							}, nil
						}
						return false, nil, nil
					})
				}
			}

			for lcluster, err := range tt.getWorkspaceErrors {
				if _, ok := clients[lcluster]; !ok {
					clients[lcluster] = tenancyfake.NewSimpleClientset()
				}
				clients[lcluster].PrependReactor("get", "workspaces", func(action clientgotesting.Action) (bool, runtime.Object, error) {
					return true, nil, err
				})
			}

			for lcluster, d := range tt.discovery {
				if _, ok := clients[lcluster]; !ok {
					clients[lcluster] = tenancyfake.NewSimpleClientset()
				}
				clients[lcluster].Resources = d
			}

			for lcluster := range tt.discoveryErrors {
				if _, ok := clients[lcluster]; !ok {
					clients[lcluster] = tenancyfake.NewSimpleClientset()
				}
			}

			streams, _, stdout, stderr := genericclioptions.NewTestIOStreams()

			kc := &KubeConfig{
				startingConfig:       tt.config.DeepCopy(),
				currentContext:       tt.config.CurrentContext,
				shortWorkspaceOutput: tt.short,

				clusterClient: fakeTenancyClient{
					t:             t,
					clients:       clients,
					discoveryErrs: tt.discoveryErrors,
				},
				modifyConfig: func(config *clientcmdapi.Config) error {
					got = config
					return nil
				},
				IOStreams: streams,
			}
			err := kc.UseWorkspace(context.Background(), tt.param)
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

			kc := &KubeConfig{
				startingConfig: tt.config.DeepCopy(),
				currentContext: tt.config.CurrentContext,
				overrides:      tt.overrides,

				modifyConfig: func(config *clientcmdapi.Config) error {
					got = config
					return nil
				},
				IOStreams: streams,
			}
			err := kc.CreateContext(context.Background(), tt.param, tt.overwrite)
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
	t             *testing.T
	clients       map[logicalcluster.Name]*tenancyfake.Clientset
	discoveryErrs map[logicalcluster.Name]error
}

func (f fakeTenancyClient) Cluster(cluster logicalcluster.Name) tenancyclient.Interface {
	client, ok := f.clients[cluster]
	require.True(f.t, ok, "no client for cluster %s", cluster)
	return withErrorDiscovery{client, f.discoveryErrs[cluster]}
}

type withErrorDiscovery struct {
	tenancyclient.Interface

	err error
}

func (c withErrorDiscovery) Discovery() discovery.DiscoveryInterface {
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
