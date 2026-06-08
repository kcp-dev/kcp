/*
Copyright 2026 The kcp Authors.

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

package authorizer

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestShardMetricsEndpoint exercises kcp#4062: shard-wide /metrics must not
// be reachable through a workspace-scoped URL, and top-level scraping must be
// authorizable through a ClusterRoleBinding in :root referring to the
// bootstrapped system:kcp:metrics-reader role.
func TestShardMetricsEndpoint(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := kcptesting.SharedKcpServer(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	adminKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootShardCfg)
	require.NoError(t, err)

	// A workspace on the root shard so we can hit it directly via rootShardCfg
	// without having to traverse the front-proxy.
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(),
		kcptesting.WithNamePrefix("shard-metrics"), kcptesting.WithRootShard())

	// user-1 is workspace admin so it can create RBAC inside the workspace.
	framework.AdmitWorkspaceAccess(ctx, t, adminKubeClusterClient, wsPath, []string{"user-1"}, nil, true)

	// Inside the workspace, user-1 grants itself NonResourceURL /metrics.
	// Pre-fix, this leaked shard-wide metrics; post-fix it must be ignored
	// because the request is rejected before authz runs.
	user1ClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", rootShardCfg))
	require.NoError(t, err)
	_, err = user1ClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace-metrics-viewer"},
		Rules: []rbacv1.PolicyRule{{
			Verbs:           []string{"get"},
			NonResourceURLs: []string{"/metrics"},
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = user1ClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "user-1-workspace-metrics-viewer"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "workspace-metrics-viewer",
		},
		Subjects: []rbacv1.Subject{{
			APIGroup: rbacv1.GroupName,
			Kind:     "User",
			Name:     "user-1",
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	workspaceMetricsPath := fmt.Sprintf("/clusters/%s/metrics", wsPath.String())

	t.Run("workspace-scoped /metrics is rejected with 501 even with matching workspace RBAC", func(t *testing.T) {
		t.Parallel()
		// Authorization is permission-cache-driven; wait until user-1's binding
		// is observable inside the workspace, otherwise the test could pass
		// for the wrong reason (403 from RBAC, not 501 from the filter).
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var code int
			_, err := user1ClusterClient.RESTClient().
				Get().AbsPath(workspaceMetricsPath).Do(ctx).StatusCode(&code).Raw()
			if code == http.StatusNotImplemented && err != nil {
				return true, ""
			}
			return false, fmt.Sprintf("status=%d err=%v", code, err)
		}, 30*time.Second, 200*time.Millisecond, "expected 501 for workspace-scoped /metrics")
	})

	t.Run("workspace-scoped /metrics is rejected for system:masters too", func(t *testing.T) {
		t.Parallel()
		var code int
		_, err := adminKubeClusterClient.RESTClient().
			Get().AbsPath(workspaceMetricsPath).Do(ctx).StatusCode(&code).Raw()
		require.Equal(t, http.StatusNotImplemented, code, "system:masters must not bypass the filter")
		require.Error(t, err)
	})

	t.Run("top-level /metrics for an unbound user is forbidden", func(t *testing.T) {
		t.Parallel()
		// user-2 has no binding anywhere.
		user2Client, err := kcpkubernetesclientset.NewForConfig(framework.StaticTokenUserConfig("user-2", rootShardCfg))
		require.NoError(t, err)
		var code int
		_, err = user2Client.RESTClient().Get().AbsPath("/metrics").Do(ctx).StatusCode(&code).Raw()
		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, code, "user without root binding must be denied")
	})

	t.Run("top-level /metrics with system:kcp:metrics-reader binding in :root succeeds", func(t *testing.T) {
		t.Parallel()
		// Bind user-3 to the bootstrap metrics-reader role inside :root.
		_, err := adminKubeClusterClient.Cluster(core.RootCluster.Path()).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "metrics-reader-user-3-"},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     bootstrap.SystemKcpMetricsReader,
			},
			Subjects: []rbacv1.Subject{{
				APIGroup: rbacv1.GroupName,
				Kind:     "User",
				Name:     "user-3",
			}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		user3Client, err := kcpkubernetesclientset.NewForConfig(framework.StaticTokenUserConfig("user-3", rootShardCfg))
		require.NoError(t, err)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var code int
			body, err := user3Client.RESTClient().Get().AbsPath("/metrics").Do(ctx).StatusCode(&code).Raw()
			if err != nil {
				return false, fmt.Sprintf("err=%v code=%d", err, code)
			}
			if code != http.StatusOK {
				return false, fmt.Sprintf("status=%d", code)
			}
			// Prometheus exposition starts with "# HELP" or "# TYPE" lines;
			// either way the kcp/k8s apiserver always emits a few metric lines.
			if !strings.Contains(string(body), "apiserver_") {
				return false, "no apiserver_ metrics in body"
			}
			return true, ""
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "user-3 should be able to scrape /metrics after root binding propagates")
	})

	t.Run("top-level /metrics for system:masters succeeds", func(t *testing.T) {
		t.Parallel()
		var code int
		body, err := adminKubeClusterClient.RESTClient().Get().AbsPath("/metrics").Do(ctx).StatusCode(&code).Raw()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, string(body), "apiserver_")
	})
}
