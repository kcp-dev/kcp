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

package workspaces

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	virtualcommand "github.com/kcp-dev/kcp/cmd/virtual-workspaces/command"
	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/softimpersonation"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testDataType struct {
	workspace1, workspace2 *tenancyv1beta1.Workspace
}

func newTestData() testDataType {
	suffix := fmt.Sprintf("-%d", rand.Intn(1000000))
	return testDataType{
		workspace1: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1" + suffix}},
		workspace2: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2" + suffix}},
	}
}

func createWorkspaceAccessRoleForGroup(t *testing.T, ctx context.Context, kubeClusterClient kcpkubernetesclientset.ClusterInterface, orgClusterName logicalcluster.Name, admin bool, groupNames ...string) {
	roleName := "org-" + orgClusterName.Base() + "-access"
	if admin {
		roleName += "-admin"
	}
	createWorkspaceAccessRoleForGroupWithCustomName(t, ctx, kubeClusterClient, orgClusterName, admin, roleName, groupNames...)
}

func createWorkspaceAccessRoleForGroupWithCustomName(t *testing.T, ctx context.Context, kubeClusterClient kcpkubernetesclientset.ClusterInterface, orgClusterName logicalcluster.Name, admin bool, roleName string, groupNames ...string) {
	parent, hasParent := orgClusterName.Parent()
	require.True(t, hasParent, "org cluster %s should have a parent", orgClusterName)

	t.Logf("Giving groups %v member access to workspace %q in %q", groupNames, orgClusterName.Base(), parent)

	contentVerbs := []string{"access"}
	if admin {
		contentVerbs = append(contentVerbs, "admin")
	}
	_, err := kubeClusterClient.Cluster(parent).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         contentVerbs,
				Resources:     []string{"workspaces/content"},
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

func createWorkspaceRoleForGroup(t *testing.T, ctx context.Context, kubeClusterClient kcpkubernetesclientset.ClusterInterface, roleName string, orgClusterName logicalcluster.Name, rules []rbacv1.PolicyRule, groupNames ...string) {
	t.Logf("Giving groups %v permissions %v in workspace %q", groupNames, rules, orgClusterName)

	_, err := kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: rules,
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
	_, err = kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err, "Failed giving groups %v permissions %v in workspace %q", groupNames, rules, orgClusterName)
}

func TestStandaloneWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	if len(framework.TestConfig.KCPKubeconfig()) != 0 {
		t.Skip("Skip testing standalone when running against persistent fixture to minimize test execution cost for development")
	}

	t.Skip("TODO reenable this test as part of the work to resolve https://github.com/kcp-dev/kcp/issues/2056")

	t.Run("Standalone virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, true)
	})
}

func TestInProcessWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	t.Run("In-process virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, false)
	})
}

type runningServer struct {
	framework.RunningServer
	orgClusterName        logicalcluster.Name
	kubeClusterClient     kcpkubernetesclientset.ClusterInterface
	kcpClusterClient      kcpclientset.ClusterInterface
	virtualUserKcpClients []VirtualClusterClient
	UserKcpClients        []kcpclientset.ClusterInterface
}

var testCases = []struct {
	name       string
	userTokens []string
	work       func(ctx context.Context, t *testing.T, server runningServer)
}{
	{
		name:       "create a workspace in an org as org content admin, and have only its creator list it, not another user with just access",
		userTokens: []string{"user-1-token", "user-2-token", "user-3-token"},
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			testData := newTestData()

			user1Client := server.UserKcpClients[0]

			vwUser1Client := server.virtualUserKcpClients[0]
			vwUser2Client := server.virtualUserKcpClients[1]
			vwUser3Client := server.virtualUserKcpClients[2]

			createWorkspaceAccessRoleForGroupWithCustomName(t, ctx, server.kubeClusterClient, server.orgClusterName, true, "org-"+server.orgClusterName.Base()+"-team-1-access", "team-1")
			createWorkspaceAccessRoleForGroupWithCustomName(t, ctx, server.kubeClusterClient, server.orgClusterName, true, "org-"+server.orgClusterName.Base()+"-team-2-access", "team-2")
			createWorkspaceAccessRoleForGroupWithCustomName(t, ctx, server.kubeClusterClient, server.orgClusterName, false, "org-"+server.orgClusterName.Base()+"-team-3-access", "team-3")

			t.Logf("Create Workspace workspace1 in the virtual workspace")
			var workspace1 *tenancyv1beta1.Workspace
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				var err error
				workspace1, err = vwUser1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				if err != nil {
					t.Logf("Failed to create workspace1: %v", err)
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

			t.Logf("Workspace will show up in list of user1")
			require.Eventually(t, func() bool {
				list, err := vwUser1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("failed to get workspaces: %v", err)
				}
				return len(list.Items) == 1 && list.Items[0].Name == workspace1.Name
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspace1")

			t.Logf("Workspace will show up in list of user2")
			require.Eventually(t, func() bool {
				list, err := vwUser2Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("failed to get workspaces: %v", err)
				}
				return len(list.Items) == 1 && list.Items[0].Name == workspace1.Name
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspace1")

			t.Logf("Workspace will also show up when user1 submits a list to KCP itself (through projection)")
			list, err := user1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "expected to list workspaces from KCP through projection as org admin user1")
			require.True(t, len(list.Items) == 1 && list.Items[0].Name == workspace1.Name, "expected to get workspace1 from KCP through projection as org admin user1")

			t.Logf("Workspace will also show up when user2 submits a list to KCP itself (through projection)")
			list, err = vwUser2Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "expected to list workspaces from KCP through projection as org admin user2")
			require.True(t, len(list.Items) == 1 && list.Items[0].Name == workspace1.Name, "expected to get workspace1 from KCP through projection as org admin user2")

			t.Logf("Workspace will not show up in list of user3 (who has only org access, and is not admin)")
			list, err = vwUser3Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Logf("failed to get workspaces: %v", err)
			}
			require.Equal(t, 0, len(list.Items), "expected to see no workspaces as user 3")
		},
	},
	{
		name:       "create a universal workspace and verify that the workspace list is empty, but does not error",
		userTokens: []string{"user-1-token"},
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			testData := newTestData()

			vwUser1Client := server.virtualUserKcpClients[0]

			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, false, "team-1")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user1-workspace-create", server.orgClusterName, []rbacv1.PolicyRule{
				{
					Verbs:     []string{"create"},
					Resources: []string{"workspaces"},
					APIGroups: []string{"tenancy.kcp.dev"},
				},
			}, "team-1")

			t.Logf("Create Workspace workspace1 in the virtual workspace")
			var workspace1 *tenancyv1beta1.Workspace
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				var err error
				workspace1, err = vwUser1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				if err != nil {
					t.Logf("Failed to create workspace1: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

			t.Logf("Wait until informer based virtual workspace sees the new workspace")
			require.Eventually(t, func() bool {
				_, err := vwUser1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to get workspace1")

			var err error
			require.Eventually(t, func() bool {
				_, err = vwUser1Client.Cluster(server.orgClusterName.Join(workspace1.Name)).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspaces in the universal cluster")
			require.NoError(t, err, "failed to list workspaces in the universal cluster")
		},
	},
	{
		name:       "create a workspace of custom type and verify that clusteworkspacetype use authorization takes place",
		userTokens: []string{"user-1-token", "user-2-token"},
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			testData := newTestData()

			vwUser1Client := server.virtualUserKcpClients[0]
			vwUser2Client := server.virtualUserKcpClients[1]

			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, false, "team-1", "team-2")
			parentCluster := framework.NewWorkspaceFixture(t, server, server.orgClusterName)
			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, parentCluster, false, "team-1", "team-2")

			t.Logf("Give user1 the right to create a workspace in the parent")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user-1-workspace-create", parentCluster, []rbacv1.PolicyRule{
				{
					Verbs:     []string{"create"},
					Resources: []string{"workspaces"},
					APIGroups: []string{"tenancy.kcp.dev"},
				},
			}, "team-1")

			t.Logf("Create custom ClusterWorkspaceType 'custom'")
			cwt, err := server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{Name: "custom"},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create custom ClusterWorkspaceType 'custom'")
			server.Artifact(t, func() (runtime.Object, error) {
				return server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, "custom", metav1.GetOptions{})
			})
			t.Logf("Wait for type custom to be usable")
			cwtName := cwt.Name
			framework.EventuallyReady(t, func() (conditions.Getter, error) {
				return server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, cwtName, metav1.GetOptions{})
			}, "could not wait for readiness on ClusterWorkspaceType %s|%s", parentCluster.String(), cwtName)

			t.Logf("Give user1 access to the custom type")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user1-custom-type-access", parentCluster, []rbacv1.PolicyRule{
				{
					Verbs:         []string{"use"},
					Resources:     []string{"clusterworkspacetypes"},
					ResourceNames: []string{"custom"},
					APIGroups:     []string{"tenancy.kcp.dev"},
				},
			}, "team-1")

			t.Logf("Create Workspace workspace1 in the virtual workspace as user1")
			var workspace1 *tenancyv1beta1.Workspace
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				var err error
				workspace1, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: testData.workspace1.Name},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "custom",
							Path: logicalcluster.From(cwt).String(),
						}},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Logf("error creating workspace: %v", err)
				}
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1 as user1")

			t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
			_, err = server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
			server.Artifact(t, func() (runtime.Object, error) {
				return server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
			})
			require.Equal(t, tenancyv1alpha1.ClusterWorkspaceTypeReference{
				Name: "custom",
				Path: logicalcluster.From(cwt).String(),
			}, workspace1.Spec.Type, "expected workspace1 to be of type custom")

			t.Logf("Create Workspace workspace2 in the virtual workspace")

			t.Logf("Try to create custom workspace as user2")
			_, err = vwUser2Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: testData.workspace2.Name},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "custom",
						Path: logicalcluster.From(cwt).String(),
					}},
				},
			}, metav1.CreateOptions{})
			require.Errorf(t, err, "expected to fail to create workspace2 as user2")

			t.Logf("Try to create custom2 workspace as user1")
			_, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: testData.workspace2.Name},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "custom2",
						Path: logicalcluster.From(cwt).String(),
					}},
				},
			}, metav1.CreateOptions{})
			require.Errorf(t, err, "expected to fail to create workspace2 as user1")
		},
	},
	{
		name:       "create a sub-workspace and verify that only users with right permissions can access workspaces inside it",
		userTokens: []string{"user-1-token", "user-2-token"},
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			testData := newTestData()

			vwUser1Client := server.virtualUserKcpClients[0]
			vwUser2Client := server.virtualUserKcpClients[1]

			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, false, "team-1", "team-2")
			parentCluster := framework.NewWorkspaceFixture(t, server, server.orgClusterName)
			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, parentCluster, false, "team-1", "team-2")

			t.Logf("Give user1 the right to use the universal cluster workspace type")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user-1-universal-type-access", parentCluster, []rbacv1.PolicyRule{
				{
					Verbs:         []string{"use"},
					Resources:     []string{"clusterworkspacetypes"},
					ResourceNames: []string{"universal"},
					APIGroups:     []string{"tenancy.kcp.dev"},
				},
			}, "team-1")

			t.Logf("Give user1 the right to create a workspace in the parent")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user1-workspace-create", parentCluster, []rbacv1.PolicyRule{
				{
					Verbs:     []string{"create"},
					Resources: []string{"workspaces"},
					APIGroups: []string{"tenancy.kcp.dev"},
				},
			}, "team-1")

			t.Logf("Give user2 the right to get workspace workspace-1")
			createWorkspaceRoleForGroup(t, ctx, server.kubeClusterClient, "user2-workspace-get-workspace1", parentCluster, []rbacv1.PolicyRule{
				{
					Verbs:         []string{"get"},
					Resources:     []string{"workspaces"},
					APIGroups:     []string{"tenancy.kcp.dev"},
					ResourceNames: []string{testData.workspace1.Name},
				},
			}, "team-2")

			t.Logf("Create Workspace workspace1 in the virtual workspace as user1")
			var workspace1 *tenancyv1beta1.Workspace
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				var err error
				workspace1, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: testData.workspace1.Name},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "universal",
							Path: "root",
						}},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Logf("error creating workspace: %v", err)
				}
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1 as user1")

			t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
			_, err := server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
			server.Artifact(t, func() (runtime.Object, error) {
				return server.kcpClusterClient.Cluster(parentCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
			})

			// Check that user1 can list and watch workspaces inside the parent workspace (part of system:kcp:tenancy:reader role every user with access has)
			t.Logf("Verify that user1 can list and watch workspaces inside the parent workspace, and get the workspace1")
			var listedWorkspaces *tenancyv1beta1.WorkspaceList
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				var err error
				listedWorkspaces, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error listing workspaces: %v", err)
				}
				return err == nil && len(listedWorkspaces.Items) == 1
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspaces inside the parent as user1")
			require.NotNil(t, listedWorkspaces, "user1 should have a non-nil result when listing in the parent workspace")
			require.Len(t, listedWorkspaces.Items, 1, "user1 should get workspace1 when listing in the parent workspace")
			require.Equal(t, listedWorkspaces.Items[0].Name, testData.workspace1.Name, "user1 should get workspace1 when listing in the parent workspace")

			_, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "user1 should be allowed to get a workspace inside the parent workspace since get permissions for the workspace owner are added by the virtual workspace")

			w, err := vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Watch(ctx, metav1.ListOptions{})
			require.NoError(t, err, "user1 should be allowed to watch workspaces inside the parent workspace due to RBAC role")
			w.Stop()

			t.Logf("Check that user1 cannot get or delete other workspaces")
			_, err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Get(ctx, "non-existing", metav1.GetOptions{})
			require.True(t, kerrors.IsForbidden(err), "expected to get a forbidden error") // not a not found error
			err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Delete(ctx, "non-existing", metav1.DeleteOptions{})
			require.True(t, kerrors.IsForbidden(err), "expected to get a forbidden error") // not a not found error

			// Check that user2 can get the `workspace1` workspace inside the parent workspace
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				_, err = vwUser2Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				if err != nil {
					t.Logf("error getting workspace: %v", err)
				}
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to get workspace 'workspace1' inside the parent as user2")
			require.Nil(t, err, "user2 should be allowed to get workspace 'workspace1' inside the parent workspace")

			listedWorkspaces, err = vwUser2Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "user2 should be allowed to list workspaces inside the parent workspace due to RBAC role")

			_, err = vwUser2Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "user2 should be allowed to get any workspace inside the parent workspace")

			err = vwUser1Client.Cluster(parentCluster).TenancyV1beta1().Workspaces().Delete(ctx, testData.workspace1.Name, metav1.DeleteOptions{})
			require.NoError(t, err, "user1 should be allowed to delete a workspace he created inside the parent workspace since delete permissions for the workspace owner are added by the virtual workspace")
		},
	},
	{
		name:       "create a workspace for an organization and don't see it in another organization",
		userTokens: []string{"user-1-token"},
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			testData := newTestData()

			org2ClusterName := framework.NewOrganizationFixture(t, server)
			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, server.orgClusterName, true, "team-1")
			createWorkspaceAccessRoleForGroup(t, ctx, server.kubeClusterClient, org2ClusterName, true, "team-1")

			org1Client := server.virtualUserKcpClients[0].Cluster(server.orgClusterName)
			org2Client := server.virtualUserKcpClients[0].Cluster(org2ClusterName)

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
			_, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
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

			t.Logf("Workspace2 will show up via get")
			require.Eventually(t, func() bool {
				// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
				_, err := org2Client.TenancyV1beta1().Workspaces().Get(ctx, workspace2.Name, metav1.GetOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see workspace1 in org1 via get")

			t.Logf("Workspace2 will show up via list in org1, workspace1 won't")
			require.Eventually(t, func() bool {
				list, err := org2Client.TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("failed to create workspace2 in org2: %v", err)
					return false
				}
				return len(list.Items) == 1 && list.Items[0].Name == workspace2.Name
			}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see workspace1 in org1 via list")
		},
	},
}

func testWorkspacesVirtualWorkspaces(t *testing.T, standalone bool) {
	var server framework.RunningServer
	var virtualWorkspaceServerHost string
	if standalone {
		// create port early. We have to hope it is still free when we are ready to start the virtual workspace apiserver.
		portStr, err := framework.GetFreePort(t)
		require.NoError(t, err)

		tokenAuthFile := framework.WriteTokenAuthFile(t)
		server = framework.PrivateKcpServer(t,
			framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile),
				"--run-virtual-workspaces=false",
				fmt.Sprintf("--shard-virtual-workspace-url=https://localhost:%s", portStr),
			)...,
			))

		// write kubeconfig to disk, next to kcp kubeconfig
		kcpAdminConfig, _ := server.RawConfig()
		var baseCluster = *kcpAdminConfig.Clusters["base"] // shallow copy
		baseCluster.Server = fmt.Sprintf("%s/clusters/system:admin", baseCluster.Server)
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
				"virtualworkspace": kcpAdminConfig.AuthInfos["shard-admin"],
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
			err = virtualcommand.Run(ctx, opts)
			require.NoError(t, err)
		}()

		// wait for readiness
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		baseClusterServerURL, err := url.Parse(baseCluster.Server)
		require.NoError(t, err)
		virtualWorkspaceServerHost = fmt.Sprintf("https://%s:%s", baseClusterServerURL.Hostname(), portStr)

		require.Eventually(t, func() bool {
			resp, err := client.Get(fmt.Sprintf("%s/readyz", virtualWorkspaceServerHost))
			if err != nil {
				t.Logf("error checking virtual workspace readiness: %v", err)
				return false
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
			t.Logf("virtual workspace is not ready yet, status code: %d", resp.StatusCode)
			return false
		}, wait.ForeverTestTimeout, time.Millisecond*100, "virtual workspace apiserver not ready")
	} else {
		server = framework.SharedKcpServer(t)
		virtualWorkspaceServerHost = server.BaseConfig(t).Host
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create non-virtual clients
			kcpConfig := server.BaseConfig(t)
			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			vwConfig := rest.CopyConfig(kcpConfig)
			vwConfig.Host = virtualWorkspaceServerHost

			// create virtual clients for all paths and users requested
			var virtualUserlKcpClients []VirtualClusterClient
			var userKcpClients []kcpclientset.ClusterInterface
			for _, token := range testCase.userTokens {
				userKcpClient, err := kcpclientset.NewForConfig(framework.ConfigWithToken(token, rest.CopyConfig(kcpConfig)))
				require.NoError(t, err, "failed to construct client for server")
				userKcpClients = append(userKcpClients, userKcpClient)
				virtualUserlKcpClients = append(virtualUserlKcpClients, &virtualClusterClient{
					config: framework.ConfigWithToken(token, rest.CopyConfig(vwConfig)),
				})
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:         server,
				orgClusterName:        orgClusterName,
				kubeClusterClient:     kubeClusterClient,
				kcpClusterClient:      kcpClusterClient,
				UserKcpClients:        userKcpClients,
				virtualUserKcpClients: virtualUserlKcpClients,
			})
		})
	}
}

func TestRootWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	user1KcpClusterClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", cfg))
	require.NoError(t, err)
	user2KcpClusterClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-2", cfg))
	require.NoError(t, err)

	tests := map[string]func(t *testing.T){
		"a user can list workspaces at the root": func(t *testing.T) {
			_, err := user1KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
		},
		"a user cannot create workspaces at the root": func(t *testing.T) {
			_, err := user1KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-workspace",
				},
			}, metav1.CreateOptions{})
			require.Error(t, err)
			require.True(t, kerrors.IsForbidden(err))
		},
		"a user sees his own workspaces at the root, but no other workspaces": func(t *testing.T) {
			// create workspace on-behalf of user-1 and user-2 via soft-impersonation (needs a system:masters client)
			impersonatedUser1Config, err := softimpersonation.WithSoftImpersonatedConfig(server.RootShardSystemMasterBaseConfig(t), &kuser.DefaultInfo{Name: "user-1"})
			require.NoError(t, err)
			impersonatedUser2Config, err := softimpersonation.WithSoftImpersonatedConfig(server.RootShardSystemMasterBaseConfig(t), &kuser.DefaultInfo{Name: "user-2"})
			require.NoError(t, err)

			impersonatedUser1ClusterClient, err := kcpclientset.NewForConfig(impersonatedUser1Config)
			require.NoError(t, err)
			impersonatedUser2ClusterClient, err := kcpclientset.NewForConfig(impersonatedUser2Config)
			require.NoError(t, err)

			t.Logf("Create workspace for user-1")
			ws1, err := impersonatedUser1ClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "user1-workspace-",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("Create workspace for user-2")
			ws2, err := impersonatedUser2ClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "user2-workspace-",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Cleanup(func() {
				kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, ws1.Name, metav1.DeleteOptions{}) //nolint:errcheck
				kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, ws2.Name, metav1.DeleteOptions{}) //nolint:errcheck
			})

			framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, tenancyv1alpha1.RootCluster.Join(ws1.Name), []string{"user-1"}, nil, []string{"access"})
			framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, tenancyv1alpha1.RootCluster.Join(ws2.Name), []string{"user-2"}, nil, []string{"access"})

			t.Logf("Wait until user-1 sees its workspace")
			framework.Eventually(t, func() (bool, string) {
				wss, err := user1KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				found := false
				for _, ws := range wss.Items {
					if ws.Name == ws1.Name {
						found = true
					}
					require.NotEqual(t, ws.Name, ws2.Name, "user-1 should not see user-2's workspace")
				}
				return found, fmt.Sprintf("expected to see workspace %s, got %v", ws1.Name, wss.Items)
			}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should see only one workspace")

			t.Logf("Wait until user-2 sees its workspace")
			framework.Eventually(t, func() (bool, string) {
				wss, err := user2KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				found := false
				for _, ws := range wss.Items {
					if ws.Name == ws2.Name {
						found = true
					}
					require.NotEqual(t, ws.Name, ws1.Name, "user-2 should not see user-1's workspace")
				}
				return found, fmt.Sprintf("expected to see workspace %s, got %v", ws2.Name, wss.Items)
			}, wait.ForeverTestTimeout, time.Millisecond*100, "user-2 should see only one workspace")

			t.Logf("Doublecheck that user-1 still sees only its own workspace")
			wss, err := user1KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			for _, ws := range wss.Items {
				require.NotEqual(t, ws.Name, ws2.Name)
			}
		},
	}

	for tcName, tcFunc := range tests {
		tcName := tcName
		tcFunc := tcFunc
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			tcFunc(t)
		})
	}
}

type VirtualClusterClient interface {
	Cluster(cluster logicalcluster.Name) clientset.Interface
}

type virtualClusterClient struct {
	config *rest.Config
}

func (c *virtualClusterClient) Cluster(cluster logicalcluster.Name) clientset.Interface {
	config := rest.CopyConfig(c.config)
	config.Host += path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", cluster.String())
	return clientset.NewForConfigOrDie(config)
}
