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

package workspace

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceTypes(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
		kcpClusterClient kcpclientset.ClusterInterface
		orgPath          logicalcluster.Path
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace without an explicit type, get default type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				t.Logf("Create a workspace without explicit type")
				workspace, err := server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(server.orgPath).Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "myapp"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(server.orgPath).Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(server.orgPath).Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")

				t.Logf("Expect workspace to be of universal type, and no initializers")
				workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(server.orgPath).Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err, "failed to get workspace")
				require.Equalf(t, workspace.Spec.Type, tenancyv1alpha1.WorkspaceTypeReference{
					Name: "universal",
					Path: "root",
				}, "workspace type is not universal")
				require.Emptyf(t, workspace.Status.Initializers, "workspace has initializers")
			},
		},
		{
			name: "create a workspace with an explicit non-existing type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				universalPath, _ := framework.NewWorkspaceFixture(t, server, core.RootCluster.Path())
				t.Logf("Create a workspace with explicit non-existing type")
				workspace, err := server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Create(ctx, &tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Type: tenancyv1alpha1.WorkspaceTypeReference{
							Name: "foo",
							Path: "root",
						},
					},
				}, metav1.CreateOptions{})
				require.Error(t, err, "failed to create workspace")

				t.Logf("Create type Foo")
				wt, err := server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				wtName := wt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, wtName, metav1.GetOptions{})
				}, "could not wait for readiness on WorkspaceType %s|%s", universalPath.String(), wtName)

				t.Logf("Create workspace with explicit type Foo again")
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Create(ctx, &tenancyv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec: tenancyv1alpha1.WorkspaceSpec{Type: tenancyv1alpha1.WorkspaceTypeReference{
							Name: tenancyv1alpha1.TypeName(wt.Name),
							Path: logicalcluster.From(wt).String(),
						}},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, "myapp", metav1.GetOptions{})
				})
				require.Equal(t, workspace.Spec.Type, tenancyv1alpha1.WorkspaceTypeReference{
					Name: "foo",
					Path: logicalcluster.From(wt).String(),
				})

				t.Logf("Expect workspace to become ready because there are no initializers")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with an explicit type allowed to be used by user-1 without having general access to it",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				universalPath, _ := framework.NewWorkspaceFixture(t, server, core.RootCluster.Path())
				typeSourcePath, _ := framework.NewWorkspaceFixture(t, server, core.RootCluster.Path())

				cfg := server.RunningServer.BaseConfig(t)
				kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
				require.NoError(t, err, "failed to construct kube cluster client for server")

				framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, universalPath, []string{"user-1"}, nil, false)
				cr, crb := createClusterRoleAndBindings(
					"create-workspace-user-1",
					"user-1", "User",
					tenancyv1alpha1.SchemeGroupVersion.Group,
					"workspaces",
					"",
					[]string{"create"},
				)
				t.Logf("Admit create workspaces to user-1 in universal workspace %q", universalPath)
				_, err = kubeClusterClient.Cluster(universalPath).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(universalPath).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				require.NoError(t, err)

				cr, crb = createClusterRoleAndBindings(
					"use-workspacetype-user-1",
					"user-1", "User",
					tenancyv1alpha1.SchemeGroupVersion.Group,
					"workspacetypes",
					"bar",
					[]string{"use"},
				)
				t.Logf("Admit use workspacetypes to user-1 in typesource workspace %q", typeSourcePath)
				_, err = kubeClusterClient.Cluster(typeSourcePath).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(typeSourcePath).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Logf("Create type Bar in typesource workspace %q", typeSourcePath)
				wt, err := server.kcpClusterClient.Cluster(typeSourcePath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "bar"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")

				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(typeSourcePath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, "bar", metav1.GetOptions{})
				})
				t.Logf("Wait for type Bar to be usable in typesource workspace %q", typeSourcePath)
				wtName := wt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(typeSourcePath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, wtName, metav1.GetOptions{})
				}, "could not wait for readiness on WorkspaceType %s|%s", universalPath.String(), wtName)

				user1KcpClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", rest.CopyConfig(cfg)))
				require.NoError(t, err, "failed to construct client for user-1")
				_ = user1KcpClient

				t.Logf("Create workspace \"myapp\" as user-1 in universal workspace %q using type \"bar\" declared in typesource workspace %q without user-1 having general access to it", universalPath, typeSourcePath)
				var workspace *tenancyv1alpha1.Workspace
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = user1KcpClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Create(ctx, &tenancyv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec: tenancyv1alpha1.WorkspaceSpec{Type: tenancyv1alpha1.WorkspaceTypeReference{
							Name: tenancyv1alpha1.TypeName(wt.Name),
							Path: logicalcluster.From(wt).String(),
						}},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().Workspaces().Get(ctx, "myapp", metav1.GetOptions{})
				})
				require.Equal(t, workspace.Spec.Type,
					tenancyv1alpha1.WorkspaceTypeReference{
						Name: "bar",
						Path: logicalcluster.From(wt).String(),
					})

				t.Logf("Expect workspace %q of type \"foo\" to become ready because there are no initializers", workspace.Name)
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with a type that has an initializer",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				universalPath, _ := framework.NewWorkspaceFixture(t, server, core.RootCluster.Path())
				t.Logf("Create type Foo with an initializer")
				wt, err := server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
					Spec: tenancyv1alpha1.WorkspaceTypeSpec{
						Initializer: true,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				wtName := wt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, wtName, metav1.GetOptions{})
				}, "could not wait for readiness on WorkspaceType %s|%s", universalPath.String(), wtName)

				t.Logf("Create workspace with explicit type Foo")
				var workspace *tenancyv1alpha1.Workspace
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Create(ctx, &tenancyv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec: tenancyv1alpha1.WorkspaceSpec{
							Type: tenancyv1alpha1.WorkspaceTypeReference{
								Name: "foo",
								Path: logicalcluster.From(wt).String(),
							},
						},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, "myapp", metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be stuck in initializing phase")
				framework.Eventually(t, func() (success bool, reason string) {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false, err.Error()
					}
					if actual, expected := workspace.Status.Phase, corev1alpha1.LogicalClusterPhaseInitializing; actual != expected {
						return false, fmt.Sprintf("workspace phase was %s, not %s", actual, expected)
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for new workspace to be stuck in initializing")

				t.Logf("Remove initializer")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					logicalCluster, err := server.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.Name(workspace.Spec.Cluster).Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
					require.NoError(t, err)
					logicalCluster.Status.Initializers = initialization.EnsureInitializerAbsent(initialization.InitializerForType(wt), logicalCluster.Status.Initializers)
					_, err = server.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.Name(workspace.Spec.Cluster).Path()).UpdateStatus(ctx, logicalCluster, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				t.Logf("Expect workspace to become ready after initializers are done")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(universalPath).Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
	}

	server := framework.SharedKcpServer(t)

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgPath, _ := framework.NewOrganizationFixture(t, server)

			cfg := server.BaseConfig(t)
			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:    server,
				kcpClusterClient: kcpClusterClient,
				orgPath:          orgPath,
			})
		})
	}
}

func createClusterRoleAndBindings(name, subjectName, subjectKind, apiGroup, resource, resourceName string, verbs []string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     verbs,
				APIGroups: []string{apiGroup},
				Resources: []string{resource},
			},
		},
	}

	if resourceName != "" {
		clusterRole.Rules[0].ResourceNames = []string{resourceName}
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: subjectKind,
				Name: subjectName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     name,
		},
	}
	return clusterRole, clusterRoleBinding
}
