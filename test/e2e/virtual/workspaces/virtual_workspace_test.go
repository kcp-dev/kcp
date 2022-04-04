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

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
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
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testDataType struct {
	workspace1, workspace1Disambiguited, workspace2, workspace2Disambiguited *tenancyv1beta1.Workspace
}

var testData = testDataType{
	workspace1:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1"}},
	workspace1Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1--1"}},
	workspace2:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2"}},
	workspace2Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2--1"}},
}

// TODO: move this into a controller and remove this method
func createOrgMemberRoleForGroup(t *testing.T, ctx context.Context, kubeClusterClient kubernetes.ClusterInterface, orgClusterName logicalcluster.LogicalCluster, groupNames ...string) {
	parent, hasParent := orgClusterName.Parent()
	require.True(t, hasParent, "org cluster %s should have a parent", orgClusterName)

	t.Logf("Giving groups %v member access to workspace %q in %q", groupNames, orgClusterName.Base(), parent)

	roleName := "org-" + orgClusterName.Base() + "-member"
	_, err := kubeClusterClient.Cluster(parent).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"access", "member"},
				Resources:     []string{"clusterworkspaces/content"},
				ResourceNames: []string{orgClusterName.Base()},
				APIGroups:     []string{"tenancy.kcp.dev"},
			},
			{
				Verbs:         []string{"get"},
				Resources:     []string{"clusterworkspaces/workspace"},
				ResourceNames: []string{orgClusterName.Base()},
				APIGroups:     []string{"tenancy.kcp.dev"},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

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
	_, err = kubeClusterClient.Cluster(parent).RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
}

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	if len(framework.TestConfig.Kubeconfig()) == 0 {
		// Skip testing standalone when running against persistent fixture to minimize
		// test execution cost for development.
		t.Run("Standalone virtual workspace apiserver", func(t *testing.T) {
			t.Parallel()
			testWorkspacesVirtualWorkspaces(t, true)
		})
	}
	t.Run("In-process virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, false)
	})
}

func testWorkspacesVirtualWorkspaces(t *testing.T, standalone bool) {
	type clientInfo struct {
		Token  string
		Prefix string
	}

	type runningServer struct {
		framework.RunningServer
		orgClusterName               logicalcluster.LogicalCluster
		kubeClusterClient            kubernetes.ClusterInterface
		kcpClusterClient             kcpclientset.ClusterInterface
		virtualKcpClients            []kcpclientset.Interface
		virtualWorkspaceExpectations []framework.RegisterWorkspaceListExpectation
	}

	var testCases = []struct {
		name                           string
		virtualWorkspaceClientContexts func(orgName logicalcluster.LogicalCluster) []clientInfo
		work                           func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace in personal virtual workspace and have only its owner list it",
			virtualWorkspaceClientContexts: func(orgName logicalcluster.LogicalCluster) []clientInfo {
				return []clientInfo{
					{
						Token:  "user-1-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName.String(), "personal"),
					},
					{
						Token:  "user-2-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName.String(), "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualKcpClients[0]
				vwUser2Client := server.virtualKcpClients[1]

				createOrgMemberRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, "team-1", "team-2")

				t.Logf("Create Workspace workspace1 in the virtual workspace")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
					if err != nil {
						klog.Errorf("Failed to create workspace1: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create Workspace workspace2 in the virtual workspace")
				var workspace2 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace2, err = vwUser2Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
					if err != nil {
						t.Logf("failed to create workspace2: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace2")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace2.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace2 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace2.Name, metav1.GetOptions{})
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
			name: "create a workspace of custom type and verify that clusteworkspacetype use authorization takes place",
			virtualWorkspaceClientContexts: func(orgName logicalcluster.LogicalCluster) []clientInfo {
				return []clientInfo{
					{
						Token:  "user-1-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName.String(), "personal"),
					},
					{
						Token:  "user-2-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName.String(), "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualKcpClients[0]
				vwUser2Client := server.virtualKcpClients[1]

				createOrgMemberRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, "team-1", "team-2")

				t.Logf("Create custom ClusterWorkspaceType 'Custom'")
				_, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{ObjectMeta: metav1.ObjectMeta{Name: "custom"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create custom ClusterWorkspaceType 'Custom'")

				t.Logf("Give user1 access to the custom type")
				_, err = server.kubeClusterClient.Cluster(server.orgClusterName).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
					ObjectMeta: metav1.ObjectMeta{
						Name: "custom-type-access",
					},
					Rules: []rbacv1.PolicyRule{
						{
							Verbs:         []string{"use", "member"},
							Resources:     []string{"clusterworkspacetypes"},
							ResourceNames: []string{"custom"},
							APIGroups:     []string{"tenancy.kcp.dev"},
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create custom ClusterRole 'custom-type-access'")

				_, err = server.kubeClusterClient.Cluster(server.orgClusterName).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "user1-custom-type-access",
					},
					RoleRef: rbacv1.RoleRef{
						Kind:     "ClusterRole",
						APIGroup: "rbac.authorization.k8s.io",
						Name:     "custom-type-access",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind: "User",
							Name: "user-1",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create custom ClusterRoleBinding 'user1-custom-type-access'")

				t.Logf("Create Workspace workspace1 in the virtual workspace as user1")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{Name: "workspace1"},
						Spec: tenancyv1beta1.WorkspaceSpec{
							Type: "Custom",
						},
					}, metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1 as user1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})
				require.Equal(t, "Custom", workspace1.Spec.Type, "expected workspace1 to be of type Custom")

				t.Logf("Create Workspace workspace2 in the virtual workspace")

				t.Logf("Try to create custom workspace as user2")
				_, err = vwUser2Client.TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: "workspace2"},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: "Custom",
					},
				}, metav1.CreateOptions{})
				require.Errorf(t, err, "expected to fail to create workspace2 as user2")

				t.Logf("Try to create custom2 workspace as user1")
				_, err = vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: "workspace2"},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: "Custom2",
					},
				}, metav1.CreateOptions{})
				require.Errorf(t, err, "expected to fail to create workspace2 as user1")
			},
		},
		{
			name: "create a workspace in personal virtual workspace for an organization and don't see it in another organization",
			virtualWorkspaceClientContexts: func(orgName logicalcluster.LogicalCluster) []clientInfo {
				return []clientInfo{
					{
						Token:  "user-1-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", orgName.String(), "personal"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				org1Client := server.virtualKcpClients[0]
				org1Expecter := server.virtualWorkspaceExpectations[0]

				org2ClusterName := framework.NewOrganizationFixture(t, server)
				token := "user-1-token"
				prefix := path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", org2ClusterName.String(), "personal")
				org2Client := newVirtualKcpClient(t, server.DefaultConfig(t), token, prefix)
				org2Expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, org2Client)
				require.NoError(t, err, "failed to start virtual workspace expecter")

				createOrgMemberRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, "team-1")
				createOrgMemberRoleForGroup(t, ctx, server.kubeClusterClient, org2ClusterName, "team-1")

				t.Logf("Create workspace1 in org1")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = org1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
					if err != nil {
						t.Logf("failed to create workspace1 in org1: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create workspace2 in org2")
				var workspace2 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace2, err = org2Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
					if err != nil {
						t.Logf("failed to create workspace2 in org2: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace2")

				err = org1Expecter(func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace1 created in org1")

				err = org2Expecter(func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace2 created in org2")
			},
		},
		{
			name: "Checks that the org a user is member of is visible to him when pointing to the root workspace with the all scope",
			virtualWorkspaceClientContexts: func(orgName logicalcluster.LogicalCluster) []clientInfo {
				return []clientInfo{
					{
						// Use a user unique to the test to ensure isolation from other tests
						Token:  "user-virtual-workspace-all-scope-token",
						Prefix: path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", "root", "all"),
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				orgName := server.orgClusterName.Base()

				createOrgMemberRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, "team-virtual-workspace-all-scope")

				err := server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
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
	}

	var server framework.RunningServer
	if standalone {
		// create port early. We have to hope it is still free when we are ready to start the virtual workspace apiserver.
		portStr, err := framework.GetFreePort(t)
		require.NoError(t, err)

		tokenAuthFile := framework.WriteTokenAuthFile(t)
		server = framework.PrivateKcpServer(t,
			"--run-controllers=false",
			"--unsupported-run-individual-controllers=workspace-scheduler",
			"--run-virtual-workspaces=false",
			fmt.Sprintf("--virtual-workspace-address=https://localhost:%s", portStr),
			"--token-auth-file", tokenAuthFile,
		)

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
		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)
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
	} else {
		server = framework.SharedKcpServer(t)
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create non-virtual clients
			kcpConfig := server.DefaultConfig(t)
			kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			kcpClusterClient, err := kcpclientset.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			// create virtual clients for all paths and users requested
			var virtualKcpClients []kcpclientset.Interface
			var virtualWorkspaceExpectations []framework.RegisterWorkspaceListExpectation
			for _, cc := range testCase.virtualWorkspaceClientContexts(orgClusterName) {
				client := newVirtualKcpClient(t, kcpConfig, cc.Token, cc.Prefix)
				virtualKcpClients = append(virtualKcpClients, client)

				// create expectations
				expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, client)
				require.NoError(t, err, "failed to start virtual workspace expecter")
				virtualWorkspaceExpectations = append(virtualWorkspaceExpectations, expecter)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                server,
				orgClusterName:               orgClusterName,
				kubeClusterClient:            kubeClusterClient,
				kcpClusterClient:             kcpClusterClient,
				virtualKcpClients:            virtualKcpClients,
				virtualWorkspaceExpectations: virtualWorkspaceExpectations,
			})
		})
	}
}

func newVirtualKcpClient(t *testing.T, kcpConfig *rest.Config, token, prefix string) kcpclientset.Interface {
	// create virtual clients
	virtualConfig := rest.CopyConfig(kcpConfig)
	virtualConfig.Host = virtualConfig.Host + prefix
	// Token must be defined in test/e2e/framework/auth-tokens.csv
	virtualConfig.BearerToken = token
	client, err := kcpclientset.NewForConfig(virtualConfig)
	require.NoError(t, err, "failed to construct kcp client")
	return client
}
