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
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	workspacescmd "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/virtual/helpers"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type testDataType struct {
	user1, user2, user3                                                      framework.User
	workspace1, workspace1Disambiguited, workspace2, workspace2Disambiguited *tenancyv1beta1.Workspace
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
	workspace1:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1"}},
	workspace1Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1--1"}},
	workspace2:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2"}},
	workspace2Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2--1"}},
}

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		kubeClient                     kubernetes.Interface
		kcpClient                      clientset.Interface
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		virtualWorkspaceClients        []clientset.Interface
		virtualWorkspaceExpectations   []framework.RegisterWorkspaceListExpectation
	}
	var testCases = []struct {
		name                           string
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		work                           func(ctx context.Context, t *testing.T, server runningServer)
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
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				vwUser2Client := server.virtualWorkspaceClients[1]
				workspace1, err := vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace1")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				workspace2, err := vwUser2Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace2")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1beta1().Workspaces().Get(ctx, testData.workspace2.Name, metav1.GetOptions{})
				})

				err = wait.PollImmediate(time.Millisecond*100, wait.ForeverTestTimeout, func() (done bool, err error) {
					if _, err := server.kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false, nil
						}
						return false, err
					}
					return true, nil
				})
				require.NoError(t, err, "did not see the workspace1 created and valid in KCP")

				if err := wait.PollImmediate(time.Millisecond*100, wait.ForeverTestTimeout, func() (done bool, err error) {
					if _, err := server.kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace2.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false, nil
						}
						return false, err
					}
					return true, nil
				}); err != nil {
					t.Errorf("did not see the workspace1 created and valid in KCP: %v", err)
					return
				}

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace created in personal virtual workspace")
				err = server.virtualWorkspaceExpectations[1](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace2 created in personal virtual workspace")
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
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				_, err := server.kubeClient.CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace")

				kcpServerKubeconfig, err := server.RunningServer.RawConfig()
				require.NoError(t, err, "failed to get KCP Kubeconfig")

				kubeconfigContent, err := clientcmd.Write(kcpServerKubeconfig)
				require.NoError(t, err, "failed to get KCP Kubeconfig content: %v")

				_, err = server.kubeClient.CoreV1().Secrets("default").Create(ctx, &v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeconfig",
						Namespace: "default",
					},
					StringData: map[string]string{
						"kubeconfig": string(kubeconfigContent),
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard secret")

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
				require.NoError(t, err, "failed to create workspace shard")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().WorkspaceShards().Get(ctx, "boston", metav1.GetOptions{})
				})

				workspace1, err := vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace1")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				workspaceURL := ""
				err = wait.PollImmediate(time.Millisecond*100, wait.ForeverTestTimeout, func() (done bool, err error) {
					cw, err := server.kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
					if err != nil {
						if apierrors.IsNotFound(err) {
							return false, nil
						}
						return false, err
					} else if !conditions.IsTrue(cw, tenancyv1alpha1.WorkspaceURLValid) {
						return false, fmt.Errorf("ClusterWorkspace %s is not valid: %s", cw.Name, conditions.GetMessage(cw, tenancyv1alpha1.WorkspaceURLValid))
					}
					workspaceURL = cw.Status.BaseURL
					return true, nil
				})
				require.NoError(t, err, "did not see the workspace created and valid in KCP")

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace created in personal virtual workspace")

				req := vwUser1Client.TenancyV1beta1().RESTClient().Get().Resource("workspaces").Name(workspace1.Name).SubResource("kubeconfig").Do(ctx)
				require.Nil(t, req.Error(), "error retrieving the kubeconfig for workspace %s: %v", workspace1.Name, err)

				kcpConfigCurrentContextName := kcpServerKubeconfig.CurrentContext
				kcpConfigCurrentContext := kcpServerKubeconfig.Contexts[kcpConfigCurrentContextName]
				require.NotNil(t, kcpConfigCurrentContext, "kcp Kubeconfig is invalid")

				kcpConfigCurrentCluster := kcpServerKubeconfig.Clusters[kcpConfigCurrentContext.Cluster]
				require.NotNil(t, kcpConfigCurrentCluster, "kcp Kubeconfig is invalid")

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
				require.NoError(t, err, "error writing the content of the expected kubeconfig for workspace %s", workspace1.Name)

				workspaceKubeconfigContent, err := req.Raw()
				require.NoError(t, err, "error retrieving the content of the kubeconfig for workspace %s", workspace1.Name)

				require.YAMLEq(t, string(expectedKubeconfigContent), string(workspaceKubeconfigContent))
			},
		},
	}

	const serverName = "main"

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var users []framework.User
			for _, vwClientContexts := range testCase.virtualWorkspaceClientContexts {
				users = append(users, vwClientContexts.User)
			}
			usersKCPArgs, err := framework.Users(users).ArgsForKCP(t)
			require.NoError(t, err)

			// TODO(marun) Can fixture be shared for this test?
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: serverName,
					Args: append([]string{
						"--run-controllers=false",
						"--unsupported-run-individual-controllers=workspace-scheduler",
					}, usersKCPArgs...),
				},
			)

			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

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
			require.NoError(t, err)

			virtualWorkspaceClients := []clientset.Interface{}
			virtualWorkspaceExpectations := []framework.RegisterWorkspaceListExpectation{}
			for _, vwConfig := range vwConfigs {
				vwClients, err := clientset.NewForConfig(vwConfig)
				require.NoError(t, err, "failed to construct client for server")

				virtualWorkspaceClients = append(virtualWorkspaceClients, vwClients)

				expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, vwClients)
				require.NoError(t, err, "failed to start expecter")

				virtualWorkspaceExpectations = append(virtualWorkspaceExpectations, expecter)
			}

			kcpCfg, err := server.Config()
			require.NoError(t, err)

			orgWorkspaceName, err := detectClusterName(kcpCfg, ctx)
			require.NoError(t, err, "failed to detect cluster name")

			kubeClients, err := kubernetes.NewClusterForConfig(kcpCfg)
			require.NoError(t, err, "failed to construct client for server")

			kubeClient := kubeClients.Cluster(orgWorkspaceName)
			kcpClients, err := clientset.NewClusterForConfig(kcpCfg)
			require.NoError(t, err, "failed to construct client for server")

			kcpClient := kcpClients.Cluster(orgWorkspaceName)

			testCase.work(ctx, t, runningServer{
				RunningServer:                  server,
				kubeClient:                     kubeClient,
				kcpClient:                      kcpClient,
				virtualWorkspaceClientContexts: testCase.virtualWorkspaceClientContexts,
				virtualWorkspaceClients:        virtualWorkspaceClients,
				virtualWorkspaceExpectations:   virtualWorkspaceExpectations,
			})
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
		if crd.ObjectMeta.Name == "clusterworkspaces.tenancy.kcp.dev" {
			return crd.ObjectMeta.ClusterName, nil
		}
	}
	return "", errors.New("detected no root cluster")
}
