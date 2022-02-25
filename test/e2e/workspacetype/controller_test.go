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

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestClusterWorkspaceTypes(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		orgKcpClient clientset.Interface
		orgExpect    framework.RegisterClusterWorkspaceExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace without an explicit type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace without explicit type")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "myapp"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				err = server.orgExpect(workspace, ready)
				require.NoError(t, err)

				t.Logf("Expect workspace to be of Universal type, and no initializers")
				workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err, "failed to get workspace")
				require.Equalf(t, workspace.Spec.Type, "Universal", "workspace type is not universal")
				require.Emptyf(t, workspace.Status.Initializers, "workspace has initializers")
			},
		},
		{
			name: "create a workspace with an explicit non-existing type",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace with explicit non-existing type")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
					Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: "Foo"},
				}, metav1.CreateOptions{})
				require.Error(t, err, "failed to create workspace")

				t.Logf("Create type Foo")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
					Spec:       tenancyv1alpha1.ClusterWorkspaceTypeSpec{},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")

				t.Logf("Create workspace with explicit type Foo again")
				require.Eventually(t, func() bool {
					workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: "Foo"},
					}, metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				require.Equal(t, workspace.Spec.Type, "Foo")

				t.Logf("Expect workspace to become ready because there are no initializers")
				err = server.orgExpect(workspace, ready)
				require.NoError(t, err)
			},
		},
		{
			name: "create a workspace with a type that has initializers",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create type Foo with initializers")
				_, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")

				t.Logf("Create workspace with explicit type Foo again")
				var workspace *tenancyv1alpha1.ClusterWorkspace
				require.Eventually(t, func() bool {
					workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: "Foo"},
					}, metav1.CreateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")

				t.Logf("Expect workspace to be stuck in initializing phase")
				time.Sleep(5 * time.Second)
				workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, workspace.Status.Phase, tenancyv1alpha1.ClusterWorkspacePhaseInitializing)

				t.Logf("Remove first initializer")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)
					for i, initializer := range workspace.Status.Initializers {
						if initializer == "a" {
							workspace.Status.Initializers = append(workspace.Status.Initializers[:i], workspace.Status.Initializers[i:]...)
							break
						}
					}
					_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Update(ctx, workspace, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				t.Logf("Expect workspace to be stuck in initializing phase because intializer b still exists")
				time.Sleep(5 * time.Second)
				workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, workspace.Status.Phase, tenancyv1alpha1.ClusterWorkspacePhaseInitializing)

				t.Logf("Remove second initializer")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					workspace, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)
					for i, initializer := range workspace.Status.Initializers {
						if initializer == "b" {
							workspace.Status.Initializers = append(workspace.Status.Initializers[:i], workspace.Status.Initializers[i:]...)
							break
						}
					}
					_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Update(ctx, workspace, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				t.Logf("Expect workspace to become ready after initializers are done")
				err = server.orgExpect(workspace, ready)
			},
		},
	}

	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		withDeadline, cancel := context.WithDeadline(ctx, deadline)
		t.Cleanup(cancel)
		ctx = withDeadline
	}

	const serverName = "main"
	f := framework.NewKcpFixture(t,
		framework.KcpConfig{
			Name: serverName,
		},
	)
	require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
	server := f.Servers[serverName]
	cfg, err := server.Config("system:admin")
	require.NoError(t, err)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			orgClusterName := framework.NewOrganizationFixture(t, server)

			kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
			orgExpect, err := framework.ExpectClusterWorkspaces(ctx, t, orgKcpClient)
			require.NoError(t, err, "failed to start expecter")

			testCase.work(ctx, t, runningServer{
				RunningServer: server,
				orgKcpClient:  orgKcpClient,
				orgExpect:     orgExpect,
			})
		})
	}
}

func ready(workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		return fmt.Errorf("workspace is not ready")
	}
	return nil
}
