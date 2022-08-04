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
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestClusterWorkspaceTypes(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		kcpClusterClient kcpclientset.Interface
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
				workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Create(logicalcluster.WithCluster(ctx, server.orgClusterName), &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "myapp"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, server.orgClusterName), workspace.Name, metav1.GetOptions{})
				})
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, server.orgClusterName), workspace.Name, metav1.GetOptions{})
				})

				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, server.orgClusterName), workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")

				t.Logf("Expect workspace to be of universal type, and no initializers")
				workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, server.orgClusterName), workspace.Name, metav1.GetOptions{})
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
				workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Create(logicalcluster.WithCluster(ctx, universal), &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root",
					}},
				}, metav1.CreateOptions{})
				require.Error(t, err, "failed to create workspace")

				t.Logf("Create type Foo")
				cwt, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(logicalcluster.WithCluster(ctx, universal), &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Get(logicalcluster.WithCluster(ctx, universal), "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				cwtName := cwt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Get(logicalcluster.WithCluster(ctx, universal), cwtName, metav1.GetOptions{})
				}, "could not wait for readiness on ClusterWorkspaceType %s|%s", universal.String(), cwtName)

				t.Logf("Create workspace with explicit type Foo again")
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Create(logicalcluster.WithCluster(ctx, universal), &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "myapp"},
						Spec:       tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ReferenceFor(cwt)},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("error creating workspace: %v", err)
					}
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace even with type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), "myapp", metav1.GetOptions{})
				})
				require.Equal(t, workspace.Spec.Type, tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Name: "foo",
					Path: logicalcluster.From(cwt).String(),
				})

				t.Logf("Expect workspace to become ready because there are no initializers")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace should be ready")
			},
		},
		{
			name: "create a workspace with a type that has an initializer",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				universal := framework.NewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)
				t.Logf("Create type Foo with an initializer")
				cwt, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(logicalcluster.WithCluster(ctx, universal), &tenancyv1alpha1.ClusterWorkspaceType{
					ObjectMeta: metav1.ObjectMeta{Name: "foo"},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace type")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Get(logicalcluster.WithCluster(ctx, universal), "foo", metav1.GetOptions{})
				})
				t.Logf("Wait for type Foo to be usable")
				cwtName := cwt.Name
				framework.EventuallyReady(t, func() (conditions.Getter, error) {
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Get(logicalcluster.WithCluster(ctx, universal), cwtName, metav1.GetOptions{})
				}, "could not wait for readiness on ClusterWorkspaceType %s|%s", universal.String(), cwtName)

				t.Logf("Create workspace with explicit type Foo")
				var workspace *tenancyv1alpha1.ClusterWorkspace
				require.Eventually(t, func() bool {
					// note: admission is informer based and hence would race with this create call
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Create(logicalcluster.WithCluster(ctx, universal), &tenancyv1alpha1.ClusterWorkspace{
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
					return server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), "myapp", metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be stuck in initializing phase")
				time.Sleep(5 * time.Second)
				workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), workspace.Name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, workspace.Status.Phase, tenancyv1alpha1.ClusterWorkspacePhaseInitializing)

				t.Logf("Remove initializer")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)
					workspace.Status.Initializers = initialization.EnsureInitializerAbsent(initialization.InitializerForType(cwt), workspace.Status.Initializers)
					_, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().UpdateStatus(logicalcluster.WithCluster(ctx, universal), workspace, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				t.Logf("Expect workspace to become ready after initializers are done")
				require.Eventually(t, func() bool {
					workspace, err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, universal), workspace.Name, metav1.GetOptions{})
					if err != nil {
						t.Logf("error getting workspace: %v", err)
					}
					return err == nil && workspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
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
