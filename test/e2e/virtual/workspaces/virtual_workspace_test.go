/*
Copyright 2021 The KCP Authors.

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

package workspaces

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	workspacescmd "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/virtual/helpers"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type testDataType struct {
	user1, user2, user3                                                      framework.User
	workspace1, workspace1Disambiguited, workspace2, workspace2Disambiguited *tenancyv1alpha1.Workspace
}

var testData = testDataType{
	user1: framework.User{
		Name:   "user-1",
		UID:    "1111-1111-1111-1111",
		Token:  "user-1-token",
		Groups: []string{"team-1"},
	},
	user2: framework.User{
		Name:   "user-2",
		UID:    "2222-2222-2222-2222",
		Token:  "user-2-token",
		Groups: []string{"team-2"},
	},
	user3: framework.User{
		Name:   "user-3",
		UID:    "3333-3333-3333-3333",
		Token:  "user-3-token",
		Groups: []string{"team-3"},
	},
	workspace1:              &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1"}},
	workspace1Disambiguited: &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1--1"}},
	workspace2:              &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2"}},
	workspace2Disambiguited: &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2--1"}},
}

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		kubeClient                     kubernetes.Interface
		kcpClient                      clientset.Interface
		kcpExpect                      framework.RegisterWorkspaceExpectation
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		virtualWorkspaceClients        []clientset.Interface
		virtualWorkspaceExpectations   []framework.RegisterWorkspaceListExpectation
	}
	var testCases = []struct {
		name                           string
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		work                           func(ctx context.Context, t framework.TestingTInterface, server runningServer)
	}{
		{
			name: "create a workspace in personal virtual workspace and have only its owner list it",
			virtualWorkspaceClientContexts: []helpers.VirtualWorkspaceClientContext{
				{
					User:   testData.user1,
					Prefix: "/personal",
				},
				{
					User:   testData.user2,
					Prefix: "/personal",
				},
			},
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				vwUser2Client := server.virtualWorkspaceClients[1]
				workspace1, err := vwUser1Client.TenancyV1alpha1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				assert.Equal(t, testData.workspace1.Name, workspace1.Name)
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().Workspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})
				workspace2, err := vwUser2Client.TenancyV1alpha1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				assert.Equal(t, testData.workspace2.Name, workspace2.Name)
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().Workspaces().Get(ctx, testData.workspace2.Name, metav1.GetOptions{})
				})
				if err := server.kcpExpect(workspace1, func(w *tenancyv1alpha1.Workspace) error {
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created in KCP: %v", err)
					return
				}
				if err := server.kcpExpect(workspace2, func(w *tenancyv1alpha1.Workspace) error {
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created in KCP: %v", err)
					return
				}
				if err := server.virtualWorkspaceExpectations[0](func(w *tenancyv1alpha1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created in personal virtual workspace: %v", err)
					return
				}
				if err := server.virtualWorkspaceExpectations[1](func(w *tenancyv1alpha1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created in personal virtual workspace: %v", err)
					return
				}
			},
		},
		{
			name: "create a workspace in personal virtual workspace and retrieve its kubeconfig",
			virtualWorkspaceClientContexts: []helpers.VirtualWorkspaceClientContext{
				{
					User:   testData.user1,
					Prefix: "/personal",
				},
			},
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				_, err := server.kubeClient.CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				kcpServerKubeconfig, err := server.RunningServer.RawConfig()
				if err != nil {
					t.Errorf("failed to get KCP Kubeconfig: %v", err)
					return
				}
				kubeconfigContent, err := clientcmd.Write(kcpServerKubeconfig)
				if err != nil {
					t.Errorf("failed to get KCP Kubeconfig content: %v", err)
					return
				}
				_, err = server.kubeClient.CoreV1().Secrets("default").Create(ctx, &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeconfig",
						Namespace: "default",
					},
					StringData: map[string]string{
						"kubeconfig": string(kubeconfigContent),
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace shard secret: %v", err)
					return
				}
				_, err = server.kcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "boston",
					},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{
						Credentials: v1.SecretReference{
							Name:      "kubeconfig",
							Namespace: "default",
						},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace shard: %v", err)
					return
				}
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().WorkspaceShards().Get(ctx, "boston", metav1.GetOptions{})
				})
				workspace1, err := vwUser1Client.TenancyV1alpha1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				assert.Equal(t, testData.workspace1.Name, workspace1.Name)
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().Workspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})
				workspaceURL := ""
				if err := server.kcpExpect(workspace1, func(w *tenancyv1alpha1.Workspace) error {
					if !conditions.IsTrue(w, tenancyv1alpha1.WorkspaceURLValid) {
						return fmt.Errorf("Workspace %s is not valid: %s", w.Name, conditions.GetMessage(w, tenancyv1alpha1.WorkspaceURLValid))
					}
					workspaceURL = w.Status.BaseURL
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created and valid in KCP: %v", err)
					return
				}

				if err := server.virtualWorkspaceExpectations[0](func(w *tenancyv1alpha1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see the workspace created in personal virtual workspace: %v", err)
					return
				}

				req := vwUser1Client.TenancyV1alpha1().RESTClient().Get().Resource("workspaces").Name(workspace1.Name).SubResource("kubeconfig").Do(ctx)
				if req.Error() != nil {
					t.Errorf("error retrieving the kubeconfig for workspace %s: %v", workspace1.Name, err)
				}
				kcpConfigCurrentContextName := kcpServerKubeconfig.CurrentContext
				kcpConfigCurrentContext := kcpServerKubeconfig.Contexts[kcpConfigCurrentContextName]
				if kcpConfigCurrentContext == nil {
					t.Errorf("kcp Kubeconfig is invalid:\n %+v", kcpServerKubeconfig)
					return
				}
				kcpConfigCurrentCluster := kcpServerKubeconfig.Clusters[kcpConfigCurrentContext.Cluster]
				if kcpConfigCurrentCluster == nil {
					t.Errorf("kcp Kubeconfig is invalid:\n %+v", kcpServerKubeconfig)
					return
				}

				expectedKubeconfigCluster := kcpConfigCurrentCluster.DeepCopy()
				expectedKubeconfigCluster.Server = workspaceURL
				expectedKubeconfig := &api.Config{
					CurrentContext: "personal/" + workspace1.Name,
					Contexts: map[string]*api.Context{
						"personal/" + workspace1.Name: {
							Cluster: "personal/" + workspace1.Name,
						},
					},
					Clusters: map[string]*api.Cluster{
						"personal/" + workspace1.Name: expectedKubeconfigCluster,
					},
				}
				expectedKubeconfigContent, err := clientcmd.Write(*expectedKubeconfig)
				if err != nil {
					t.Errorf("error writing the content of the expected kubeconfig for workspace %s: %v", workspace1.Name, err)
					return
				}
				workspaceKubeconfigContent, err := req.Raw()
				if err != nil {
					t.Errorf("error retrieving the content of the kubeconfig for workspace %s: %v", workspace1.Name, err)
					return
				}

				assert.YAMLEq(t, string(expectedKubeconfigContent), string(workspaceKubeconfigContent))
			},
		},
	}

	const serverName = "main"

	for i := range testCases {
		testCase := testCases[i]

		var users []framework.User
		for _, vwClientContexts := range testCase.virtualWorkspaceClientContexts {
			users = append(users, vwClientContexts.User)
		}
		usersKCPArgs, err := framework.Users(users).ArgsForKCP(t)
		if err != nil {
			t.Error(err)
			continue
		}

		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
			if len(servers) != 1 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			server := servers[serverName]

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			vw := helpers.VirtualWorkspace{
				BuildSubCommandOtions: func(kcpServer framework.RunningServer) virtualcmd.SubCommandOptions {
					return &workspacescmd.WorkspacesSubCommandOptions{
						KubeconfigFile: kcpServer.KubeconfigPath(),
						RootPathPrefix: "/",
					}
				},
				ClientContexts: testCase.virtualWorkspaceClientContexts,
			}

			vwConfigs, err := vw.Setup(t, ctx, server)
			if err != nil {
				t.Error(err.Error())
				return
			}

			virtualWorkspaceClients := []clientset.Interface{}
			virtualWorkspaceExpectations := []framework.RegisterWorkspaceListExpectation{}
			for _, vwConfig := range vwConfigs {
				vwClients, err := clientset.NewForConfig(vwConfig)
				if err != nil {
					t.Errorf("failed to construct client for server: %v", err)
					return
				}
				virtualWorkspaceClients = append(virtualWorkspaceClients, vwClients)
				expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, vwClients)
				if err != nil {
					t.Errorf("failed to start expecter: %v", err)
					return
				}
				virtualWorkspaceExpectations = append(virtualWorkspaceExpectations, expecter)
			}

			kcpCfg, err := server.Config()
			if err != nil {
				t.Error(err)
				return
			}
			clusterName, err := detectClusterName(kcpCfg, ctx)
			if err != nil {
				t.Errorf("failed to detect cluster name: %v", err)
				return
			}
			kubeClients, err := kubernetes.NewClusterForConfig(kcpCfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			kubeClient := kubeClients.Cluster(clusterName)
			kcpClients, err := clientset.NewClusterForConfig(kcpCfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			kcpClient := kcpClients.Cluster(clusterName)
			kcpExpect, err := framework.ExpectWorkspaces(ctx, t, kcpClient)
			if err != nil {
				t.Errorf("failed to start expecter: %v", err)
				return
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                  server,
				kubeClient:                     kubeClient,
				kcpClient:                      kcpClient,
				kcpExpect:                      kcpExpect,
				virtualWorkspaceClientContexts: testCase.virtualWorkspaceClientContexts,
				virtualWorkspaceClients:        virtualWorkspaceClients,
				virtualWorkspaceExpectations:   virtualWorkspaceExpectations,
			})
		}, framework.KcpConfig{
			Name: serverName,
			Args: append([]string{"--install-workspace-scheduler"}, usersKCPArgs...),
		})
	}
}

// TODO: we need to undo the prefixing and get normal sharding behavior in soon ... ?
func detectClusterName(cfg *rest.Config, ctx context.Context) (string, error) {
	crdClient, err := apiextensionsclientset.NewClusterForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to construct client for server: %w", err)
	}
	crds, err := crdClient.Cluster("*").ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list crds: %w", err)
	}
	if len(crds.Items) == 0 {
		return "", errors.New("found no crds, cannot detect cluster name")
	}
	for _, crd := range crds.Items {
		if crd.ObjectMeta.Name == "workspaces.tenancy.kcp.dev" {
			return crd.ObjectMeta.ClusterName, nil
		}
	}
	return "", errors.New("detected no admin cluster")
}
