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
	"crypto/tls"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	virtualcommand "github.com/kcp-dev/kcp/cmd/virtual-workspaces/command"
	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
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

// TODO: move this into a controller and remove this method
func createOrgMemberRoleForGroup(ctx context.Context, kubeClient kubernetes.Interface, orgClusterName string, groupNames ...string) error {
	_, orgName, err := helper.ParseLogicalClusterName(orgClusterName)
	if err != nil {
		return err
	}

	roleName := "org-" + orgName + "-member"
	if _, err := kubeClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"access", "member"},
				Resources:     []string{"clusterworkspaces/content"},
				ResourceNames: []string{orgName},
				APIGroups:     []string{"tenancy.kcp.dev"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     roleName,
		},
	}

	for _, groupName := range groupNames {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			Kind:      "Group",
			Name:      groupName,
			Namespace: "",
		})
	}
	if _, err := kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Run("Standalone virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, true)
	})
	t.Run("In-process virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, false)
	})
}

func testWorkspacesVirtualWorkspaces(t *testing.T, standalone bool) {
	type clientInfo struct {
		User   framework.User
		Prefix string
	}

	type runningServer struct {
		framework.RunningServer
		orgClusterName                string
		orgKubeClient, rootKubeClient kubernetes.Interface
		orgKcpClient, rootKcpClient   kcpclientset.Interface
		virtualKcpClients             []kcpclientset.Interface
		virtualWorkspaceExpectations  []framework.RegisterWorkspaceListExpectation
	}

	var testCases = []struct {
		name                           string
		virtualWorkspaceClientContexts func(orgName string) []clientInfo
		work                           func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace in personal virtual workspace and have only its owner list it",
			virtualWorkspaceClientContexts: func(orgName string) []clientInfo {
				return []clientInfo{
					{
						User:   testData.user1,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName, "personal"),
					},
					{
						User:   testData.user2,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName, "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualKcpClients[0]
				vwUser2Client := server.virtualKcpClients[1]

				err := createOrgMemberRoleForGroup(ctx, server.rootKubeClient, server.orgClusterName, "team-1", "team-2")
				require.NoError(t, err, "failed to create root workspace roles")

				t.Logf("Create Workspace workspace1 in the virtual workspace")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create Workspace workspace2 in the virtual workspace")
				var workspace2 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace2, err = vwUser2Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace2")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace2.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace2 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace2.Name, metav1.GetOptions{})
				})

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
			name: "create a workspace in personal virtual workspace for an organization and don't see it in another organization",
			virtualWorkspaceClientContexts: func(orgName string) []clientInfo {
				return []clientInfo{
					{
						User:   testData.user1,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName, "personal"),
					},
					{
						User:   testData.user1,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", "root:default", "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				testOrgClient := server.virtualKcpClients[0]
				defaultOrgClient := server.virtualKcpClients[1]

				err := createOrgMemberRoleForGroup(ctx, server.rootKubeClient, server.orgClusterName, "team-1")
				require.NoError(t, err, "failed to create root workspace roles")

				// TODO: move away from root:default. No e2e should depend on default object,
				// but be hermeticly separated from everything else.
				err = createOrgMemberRoleForGroup(ctx, server.rootKubeClient, "root:default", "team-1")
				require.NoError(t, err, "failed to create root workspace roles")

				t.Logf("Create Workspace workspace1 in test org")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = testOrgClient.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create Workspace workspace2 in the virtual workspace")
				var workspace2 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace2, err = defaultOrgClient.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace2")

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace1 created in test org")
				err = server.virtualWorkspaceExpectations[1](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace2 created in test org")
			},
		},
		{
			name: "Checks that the org a user is member of is visible to him when pointing to the root workspace with the all scope",
			virtualWorkspaceClientContexts: func(orgName string) []clientInfo {
				return []clientInfo{
					{
						User:   testData.user1,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", "root", "all"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				_, orgName, err := helper.ParseLogicalClusterName(server.orgClusterName)
				require.NoError(t, err, "failed to parse organization logical cluster")

				err = createOrgMemberRoleForGroup(ctx, server.rootKubeClient, server.orgClusterName, "team-1")
				require.NoError(t, err, "failed to create root workspace roles")

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					expectedOrgs := sets.NewString(orgName)
					workspaceNames := sets.NewString()
					for _, item := range w.Items {
						workspaceNames.Insert(item.Name)
					}
					if workspaceNames.Equal(expectedOrgs) {
						return nil
					}
					return fmt.Errorf("expected 1 workspaces (%#v), got %#v", expectedOrgs, w)
				})
				require.NoError(t, err, "did not see the workspace1 created in test org")
			},
		},
		{
			name: "create a workspace in personal virtual workspace and retrieve its kubeconfig",
			virtualWorkspaceClientContexts: func(orgName string) []clientInfo {
				return []clientInfo{
					{
						User:   testData.user1,
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName, "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualKcpClients[0]

				err := createOrgMemberRoleForGroup(ctx, server.rootKubeClient, server.orgClusterName, "team-1")
				require.NoError(t, err, "failed to create root workspace roles")

				kcpServerKubeconfig, err := server.RunningServer.RawConfig()
				require.NoError(t, err, "failed to get KCP Kubeconfig")

				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				require.Eventually(t, func() bool {
					ws, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace1")
					return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
				}, wait.ForeverTestTimeout, time.Millisecond*100, " workspace1 did not become ready")

				ws, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "failed to get workspace1 as ClusterWorkspace")
				workspaceURL := ws.Status.BaseURL

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
				expectedKubeconfig := &clientcmdapi.Config{
					CurrentContext: "personal/" + workspace1.Name,
					Contexts: map[string]*clientcmdapi.Context{
						"personal/" + workspace1.Name: {
							Cluster: "personal/" + workspace1.Name,
						},
					},
					Clusters: map[string]*clientcmdapi.Cluster{
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
			for _, vwClientContexts := range testCase.virtualWorkspaceClientContexts("") {
				users = append(users, vwClientContexts.User)
			}
			usersKCPArgs, err := framework.Users(users).ArgsForKCP(t)
			require.NoError(t, err)

			// create port early. We have to hope it is still free when we are ready to start the virtual workspace apiserver.
			portStr, err := framework.GetFreePort(t)
			require.NoError(t, err)

			// TODO(marun) Can fixture be shared for this test?
			var extraArgs []string
			if standalone {
				extraArgs = append(extraArgs,
					"--run-virtual-workspaces=false",
					fmt.Sprintf("--virtual-workspace-address=https://localhost:%s", portStr),
				)
			}
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: serverName,
					Args: append(append(extraArgs,
						"--run-controllers=false",
						"--unsupported-run-individual-controllers=workspace-scheduler",
					), usersKCPArgs...),
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

			orgClusterName := framework.NewOrganizationFixture(t, server)

			if standalone {
				// write kubeconfig to disk, next to kcp kubeconfig
				kcpAdminConfig, _ := server.RawConfig()
				var baseCluster = *kcpAdminConfig.Clusters["system:admin"] // shallow copy
				virtualWorkspaceKubeConfig := clientcmdapi.Config{
					Clusters: map[string]*clientcmdapi.Cluster{
						"shard": &baseCluster,
					},
					Contexts: map[string]*clientcmdapi.Context{
						"shard": {
							Cluster:  "shard",
							AuthInfo: "virtualworkspace",
						},
					},
					AuthInfos: map[string]*clientcmdapi.AuthInfo{
						"virtualworkspace": kcpAdminConfig.AuthInfos["admin"],
					},
					CurrentContext: "shard",
				}
				kubeconfigPath := filepath.Join(filepath.Dir(server.KubeconfigPath()), "virtualworkspace.kubeconfig")
				err = clientcmd.WriteToFile(virtualWorkspaceKubeConfig, kubeconfigPath)
				require.NoError(t, err)

				// launch virtual workspace apiserver
				port, err := strconv.Atoi(portStr)
				require.NoError(t, err)
				opts := virtualoptions.NewOptions()
				opts.KubeconfigFile = kubeconfigPath
				opts.SecureServing.BindPort = port
				opts.SecureServing.ServerCert.CertKey.KeyFile = filepath.Join(filepath.Dir(server.KubeconfigPath()), "apiserver.key")
				opts.SecureServing.ServerCert.CertKey.CertFile = filepath.Join(filepath.Dir(server.KubeconfigPath()), "apiserver.crt")
				opts.Authentication.SkipInClusterLookup = true
				opts.Authentication.RemoteKubeConfigFile = kubeconfigPath
				err = opts.Validate()
				require.NoError(t, err)
				go func() {
					err = virtualcommand.Run(opts, ctx.Done())
					require.NoError(t, err)
				}()

				// wait for readiness
				client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
				require.Eventually(t, func() bool {
					resp, err := client.Get(fmt.Sprintf("https://localhost:%s/readyz", portStr))
					if err != nil {
						klog.Warningf("error checking virtual workspace readiness: %v", err)
						return false
					}
					defer resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						return true
					}
					klog.Infof("virtual workspace is not ready yet, status code: %d", resp.StatusCode)
					return false
				}, wait.ForeverTestTimeout, time.Millisecond*100, "virtual workspace apiserver not ready")
			}

			// create non-virtual clients
			kcpConfig, err := server.DefaultConfig()
			require.NoError(t, err)
			kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			kcpClusterClient, err := kcpclientset.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			// create virtual clients for all paths and users requested
			var virtualKcpClients []kcpclientset.Interface
			var virtualWorkspaceExpectations []framework.RegisterWorkspaceListExpectation
			kcpRawConfig, err := server.RawConfig()
			require.NoError(t, err)
			for _, cc := range testCase.virtualWorkspaceClientContexts(orgClusterName) {
				// create virtual clients
				virtualConfig := rest.CopyConfig(kcpConfig)
				virtualConfig.Host = virtualConfig.Host + cc.Prefix
				if authInfo, exists := kcpRawConfig.AuthInfos[cc.User.Name]; exists && cc.User.Token == "" {
					virtualConfig.BearerToken = authInfo.Token
				} else {
					virtualConfig.BearerToken = cc.User.Token
				}
				client, err := kcpclientset.NewForConfig(virtualConfig)
				require.NoError(t, err, "failed to construct kcp client")
				virtualKcpClients = append(virtualKcpClients, client)

				// create expectations
				expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, client)
				require.NoError(t, err, "failed to start virtual workspace expecter")
				virtualWorkspaceExpectations = append(virtualWorkspaceExpectations, expecter)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                server,
				orgClusterName:               orgClusterName,
				orgKubeClient:                kubeClusterClient.Cluster(orgClusterName),
				orgKcpClient:                 kcpClusterClient.Cluster(orgClusterName),
				rootKubeClient:               kubeClusterClient.Cluster(helper.RootCluster),
				rootKcpClient:                kcpClusterClient.Cluster(helper.RootCluster),
				virtualKcpClients:            virtualKcpClients,
				virtualWorkspaceExpectations: virtualWorkspaceExpectations,
			})
		})
	}
}
