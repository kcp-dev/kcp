/*
Copyright 2025 The KCP Authors.

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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/integration/framework"
)

func createAndDeleteWs(ctx context.Context, t *testing.T, kcpClient kcpclientset.ClusterInterface, name string) {
	t.Logf("Create workspace %q", name)
	workspace, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Create(t.Context(), &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: "universal",
				Path: "root",
			},
			Location: &tenancyv1alpha1.WorkspaceLocation{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": corev1alpha1.RootShard,
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace %q", workspace.Name)

	t.Logf("Wait until the %q workspace is ready", workspace.Name)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		workspace, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(t.Context(), workspace.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if phase := workspace.Status.Phase; phase != corev1alpha1.LogicalClusterPhaseReady {
			return false, "workspace is not ready, is " + string(phase)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Delete workspace %q", workspace.Name)
	err = kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Delete(t.Context(), workspace.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

	t.Logf("Ensure workspace %q is removed", workspace.Name)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		ws, err := kcpClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(t.Context(), workspace.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, ""
			}
			return false, err.Error()
		}
		return ws == nil, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	// See https://github.com/kcp-dev/kcp/issues/3488
	time.Sleep(2 * time.Second)
}

var (
	// These are goroutines that can pop up randomly during KCP
	// operations and aren't indicative of a leak when deleting
	// a workspace.
	randomGoroutineSources = []goleak.Option{
		goleak.IgnoreCreatedBy("net/http.(*Server).Serve"),
		goleak.IgnoreCreatedBy("golang.org/x/net/http2.(*Transport).newClientConn"),
		goleak.IgnoreCreatedBy("golang.org/x/net/http2.(*serverConn).serve"),
	}
)

func TestWorkspaceDeletionLeak(t *testing.T) {
	_, kcpClient, _ := framework.StartTestServer(t)

	t.Logf("Create warmup workspace")
	createAndDeleteWs(t.Context(), t, kcpClient, "leak-warmup")

	t.Logf("Register current goroutines after warmup")
	curGoroutines := goleak.IgnoreCurrent()

	t.Logf("Create workspace")
	createAndDeleteWs(t.Context(), t, kcpClient, "leak-test")

	t.Logf("Check for leftover goroutines")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		if err := goleak.Find(append(randomGoroutineSources, curGoroutines)...); err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
