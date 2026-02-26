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

package plugin

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpfakeclient "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
)

func TestCreate(t *testing.T) {
	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingWorkspaces []string // existing workspaces
		skipInitialType    bool
		markReady          bool

		newWorkspaceName                 string
		newWorkspaceType                 *tenancyv1alpha1.WorkspaceTypeReference
		useAfterCreation, ignoreExisting bool
		createContextName                string

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
			markReady:        true,
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
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"test":                      {Cluster: "test", AuthInfo: "test"},
					"workspace.kcp.io/current":  {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"workspace.kcp.io/previous": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test":                     {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
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
			skipInitialType:    true,
			newWorkspaceName:   "bar",
			ignoreExisting:     true,
		},
		{
			name: "create, already existing, use after creation",
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
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"test":                      {Cluster: "test", AuthInfo: "test"},
					"workspace.kcp.io/current":  {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"workspace.kcp.io/previous": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test":                     {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
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
			newWorkspaceType:   &tenancyv1alpha1.WorkspaceTypeReference{Path: "root", Name: "universal"},
			useAfterCreation:   true,
			markReady:          true,
			ignoreExisting:     true,
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"test":                      {Cluster: "test", AuthInfo: "test"},
					"workspace.kcp.io/current":  {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"workspace.kcp.io/previous": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test":                     {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo:bar"},
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
			newWorkspaceType: &tenancyv1alpha1.WorkspaceTypeReference{Name: "universal"},
			wantErr:          true,
		},
		{
			name: "create with create-context only",
			config: clientcmdapi.Config{
				CurrentContext: "test",
				Contexts: map[string]*clientcmdapi.Context{
					"test": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			expected: &clientcmdapi.Config{
				CurrentContext: "test",
				Contexts: map[string]*clientcmdapi.Context{
					"test": {Cluster: "test", AuthInfo: "test"},
					"bar":  {Cluster: "bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test": {Server: "https://test/clusters/root:foo"},
					"bar":  {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingWorkspaces: []string{"test"},
			newWorkspaceName:   "bar",
			createContextName:  "bar",
			useAfterCreation:   false,
			markReady:          true,
		},
		{
			name: "create with create-context and enter",
			config: clientcmdapi.Config{
				CurrentContext: "test",
				Contexts: map[string]*clientcmdapi.Context{
					"test": {Cluster: "test", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test": {Server: "https://test/clusters/root:foo"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			expected: &clientcmdapi.Config{
				CurrentContext: "bar",
				Contexts: map[string]*clientcmdapi.Context{
					"test": {Cluster: "test", AuthInfo: "test"},
					"bar":  {Cluster: "bar", AuthInfo: "test"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"test": {Server: "https://test/clusters/root:foo"},
					"bar":  {Server: "https://test/clusters/root:foo:bar"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			existingWorkspaces: []string{"test"},
			newWorkspaceName:   "bar",
			createContextName:  "bar",
			useAfterCreation:   true,
			markReady:          true,
		},
	}
	for _, tt := range tests {
		// TODO(sttts): tt has a data race here due to the parallel test execution. But unaliasing it breaks the tests. WTF.
		// tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			currentClusterName := logicalcluster.NewPath(strings.TrimPrefix(u.Path, "/clusters/"))
			u.Path = ""

			objects := []runtime.Object{}
			for _, name := range tt.existingWorkspaces {
				objects = append(objects, &tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: currentClusterName.String(),
						},
						Name: name,
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL: fmt.Sprintf("https://test%s", currentClusterName.Join(name).RequestPath()),
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "universal",
							Path: "root",
						},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{
						Phase: corev1alpha1.LogicalClusterPhaseReady,
					},
				})
			}
			client := kcpfakeclient.NewSimpleClientset(objects...)

			// Fill up the resources map for the discovery client
			for _, name := range append(tt.existingWorkspaces, tt.newWorkspaceName) {
				if client.Resources == nil {
					client.Resources = map[logicalcluster.Path][]*metav1.APIResourceList{}
				}
				client.Resources[logicalcluster.NewPath(currentClusterName.String()).Join(name)] = []*metav1.APIResourceList{
					{
						GroupVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
						APIResources: []metav1.APIResource{
							{
								Name:         "workspaces",
								SingularName: "workspace",
							},
						},
					},
				}
			}

			workspaceType := tt.newWorkspaceType
			if tt.newWorkspaceType == nil {
				workspaceType = &tenancyv1alpha1.WorkspaceTypeReference{
					Name: "universal",
					Path: "root",
				}
			}

			if tt.markReady {
				client.PrependReactor("create", "workspaces", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
					obj := action.(kcptesting.CreateAction).GetObject().(*tenancyv1alpha1.Workspace)
					obj.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
					u := parseURLOrDie(u.String())
					u.Path = currentClusterName.Join(obj.Name).RequestPath()
					obj.Spec.URL = u.String()
					obj.Spec.Type = workspaceType
					if err := client.Tracker().Cluster(currentClusterName).Create(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces"), obj, ""); err != nil {
						return false, nil, err
					}
					return true, obj, nil
				})
			}

			opts := NewCreateWorkspaceOptions(genericclioptions.NewTestIOStreamsDiscard())
			opts.Name = tt.newWorkspaceName
			if !tt.skipInitialType {
				opts.Type = workspaceType.Path + ":" + string(workspaceType.Name)
			}
			if tt.createContextName != "" {
				opts.CreateContextName = tt.createContextName
			}
			opts.IgnoreExisting = tt.ignoreExisting
			opts.EnterAfterCreate = tt.useAfterCreation
			opts.ReadyWaitTimeout = time.Second
			opts.modifyConfig = func(configAccess clientcmd.ConfigAccess, config *clientcmdapi.Config) error {
				got = config
				return nil
			}
			opts.kcpClusterClient = client
			opts.newKCPClusterClient = func(config clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
				return client, nil
			}
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
