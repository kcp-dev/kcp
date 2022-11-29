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
	"strings"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	kcpapisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestClusterWorkspaceTypes(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
		kcpClusterClient kcpclientset.ClusterInterface
		orgClusterName   logicalcluster.Name
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace without an explicit type, get default type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace without explicit type")
				workspace, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "myapp"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")

				t.Logf("Expect workspace to be of universal type, and no initializers")
				workspace, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err, "failed to get workspace")
				require.Equalf(t, workspace.Spec.Type, tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Name: "universal",
					Path: "root",
				}, "workspace type is not universal")
				require.Emptyf(t, workspace.Status.Initializers, "workspace has initializers")
			},
		},
		{
			name: "create a workspace with an explicit non-existing type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				universal := framework.NewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)
				t.Logf("Create a workspace with explicit non-existing type")
				workspace, err := server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root",
					}},
				}, metav1.CreateOptions{})
				require.Error(t, err, "failed to create workspace")

				t.Logf("Create type Foo")
				cwt, err := server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				cwtName := cwt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, cwtName, metav1.GetOptions{})
				}, "could not wait for readiness on ClusterWorkspaceType %s|%s", universal.String(), cwtName)

				t.Logf("Create workspace with explicit type Foo again")
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: referenceFor(cwt)},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "myapp", metav1.GetOptions{})
				})
				require.Equal(t, workspace.Spec.Type, tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Name: "foo",
					Path: logicalcluster.From(cwt).String(),
				})

				t.Logf("Expect workspace to become ready because there are no initializers")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with an explicit type allowed to be used by user-1 without having general access to it",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				universal := framework.NewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)
				typesource := framework.NewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)

				cfg := server.BaseConfig(t)
				kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
				require.NoError(t, err, "failed to construct kube cluster client for server")

				framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, universal, []string{"user-1"}, nil, false)
				cr, crb := createClusterRoleAndBindings(
					"create-clusterworkspace-user-1",
					"user-1", "User",
					tenancyv1alpha1.SchemeGroupVersion.Group,
					"clusterworkspaces",
					"",
					[]string{"create"},
				)
				t.Logf("Admit create clusterworkspaces to user-1 in universal workspace %q", universal)
				_, err = kubeClusterClient.Cluster(universal).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(universal).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				require.NoError(t, err)

				cr, crb = createClusterRoleAndBindings(
					"apiexport-user-1-"+universal.String(),
					kcpapisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix+"user-1", "User",
					tenancyv1alpha1.SchemeGroupVersion.Group,
					"clusterworkspaces",
					"",
					[]string{"create"},
				)
				t.Logf("Admit create clusterworkspaces for api bindings to %q in root workspace %q", kcpapisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix+"user-1", tenancyv1alpha1.RootCluster)
				_, err = kubeClusterClient.Cluster(tenancyv1alpha1.RootCluster).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(tenancyv1alpha1.RootCluster).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				require.NoError(t, err)

				cr, crb = createClusterRoleAndBindings(
					"use-clusterworkspacetype-user-1",
					"user-1", "User",
					tenancyv1alpha1.SchemeGroupVersion.Group,
					"clusterworkspacetypes",
					"bar",
					[]string{"use"},
				)
				t.Logf("Admit use clusterworkspacetypes to user-1 in typesource workspace %q", typesource)
				_, err = kubeClusterClient.Cluster(typesource).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(typesource).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Logf("Create type Bar in typesource workspace %q", typesource)
				cwt, err := server.kcpClusterClient.Cluster(typesource).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "bar"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(typesource).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, "bar", metav1.GetOptions{})
				})
				t.Logf("Wait for type Bar to be usable in typesource workspace %q", typesource)
				cwtName := cwt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(typesource).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, cwtName, metav1.GetOptions{})
				}, "could not wait for readiness on ClusterWorkspaceType %s|%s", universal.String(), cwtName)

				user1KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
				require.NoError(t, err, "failed to construct client for user-1")
				_ = user1KcpClient

				t.Logf("Create workspace \"myapp\" as user-1 in universal workspace %q using type \"bar\" declared in typesource workspace %q without user-1 having general access to it", universal, typesource)
				var workspace *tenancyv1alpha1.ClusterWorkspace
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = user1KcpClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: referenceFor(cwt)},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "myapp", metav1.GetOptions{})
				})
				require.Equal(t, workspace.Spec.Type, tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Name: "bar",
					Path: logicalcluster.From(cwt).String(),
				})

				t.Logf("Expect workspace %q of type \"foo\" to become ready because there are no initializers", workspace.Name)
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with a type that has an initializer",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				universal := framework.NewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)
				t.Logf("Create type Foo with an initializer")
				cwt, err := server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				cwtName := cwt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaceTypes().Get(ctx, cwtName, metav1.GetOptions{})
				}, "could not wait for readiness on ClusterWorkspaceType %s|%s", universal.String(), cwtName)

				t.Logf("Create workspace with explicit type Foo")
				var workspace *tenancyv1alpha1.ClusterWorkspace
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: logicalcluster.From(cwt).String(),
						}},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "myapp", metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be stuck in initializing phase")
				framework.Eventually(t, func() (success bool, reason string) {
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false, err.Error()
					}
					if actual, expected := workspace.Status.Phase, tenancyv1alpha1.WorkspacePhaseInitializing; actual != expected {
						return false, fmt.Sprintf("workspace phase was %s, not %s", actual, expected)
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for new workspace to be stuck in initializing")

				t.Logf("Remove initializer")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)
					workspace.Status.Initializers = initialization.EnsureInitializerAbsent(initialization.InitializerForType(cwt), workspace.Status.Initializers)
					_, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().UpdateStatus(ctx, workspace, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				t.Logf("Expect workspace to become ready after initializers are done")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.Cluster(universal).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with deeper nesting",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				org := framework.NewOrganizationFixture(t, server)
				team := framework.NewWorkspaceFixture(t, server, org, framework.WithType(tenancyv1alpha1.RootCluster, "team"))
				universal := framework.NewWorkspaceFixture(t, server, team)

				require.Len(t, strings.Split(universal.String(), ":"), 4, "expecting root:org:team:universal, i.e. 4 levels")
				require.True(t, strings.HasPrefix(universal.String(), team.String()), "expecting universal to be a child of team")
			},
		},
	}

	server := framework.SharedKcpServer(t)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			cfg := server.BaseConfig(t)
			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:    server,
				kcpClusterClient: kcpClusterClient,
				orgClusterName:   orgClusterName,
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

func referenceFor(cwt *tenancyv1alpha1.ClusterWorkspaceType) tenancyv1alpha1.ResolvedWorkspaceTypeReference {
	return tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: tenancyv1alpha1.ClusterWorkspaceTypeReference{
		Name: tenancyv1alpha1.TypeName(cwt.Name),
		Path: logicalcluster.From(cwt).String(),
	}}
}
