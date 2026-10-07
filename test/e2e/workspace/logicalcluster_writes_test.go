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

package workspace

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestLogicalClusterWritesRestrictedToKcpAdmin verifies that a workspace admin
// cannot write to the LogicalCluster of their own workspace, while kcp-admin
// and the kcp system identities can.
func TestLogicalClusterWritesRestrictedToKcpAdmin(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	adminKcpClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	adminKubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 cluster-admin of the workspace")
	framework.AdmitWorkspaceAccess(t.Context(), t, adminKubeClient, orgPath, []string{"user-1"}, nil, false)
	framework.AdmitWorkspaceAccess(t.Context(), t, adminKubeClient, wsPath, []string{"user-1"}, nil, true)

	userKcpClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", cfg))
	require.NoError(t, err)
	userLCs := userKcpClient.Cluster(wsPath).CoreV1alpha1().LogicalClusters()

	t.Log("Wait until user-1 can read the LogicalCluster")
	var lc *corev1alpha1.LogicalCluster
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		lc, err = userLCs.Get(t.Context(), corev1alpha1.LogicalClusterName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	const annotationKey = "e2e.example.com/lc-writes"
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, annotationKey))

	// The forbidden error must come from the LogicalCluster admission plugin,
	// not from RBAC, so match on its message.
	requireAdmissionForbidden := func(name string, write func() error) {
		t.Helper()
		t.Logf("Verify that user-1 cannot %s the LogicalCluster", name)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			err := write()
			if err == nil {
				return false, fmt.Sprintf("expected %s to be forbidden, but it succeeded", name)
			}
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "may write LogicalClusters") {
				return false, fmt.Sprintf("expected forbidden by LogicalCluster admission, got: %v", err)
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	requireAdmissionForbidden("update", func() error {
		mod := lc.DeepCopy()
		if mod.Annotations == nil {
			mod.Annotations = map[string]string{}
		}
		mod.Annotations[annotationKey] = "true"
		_, err := userLCs.Update(t.Context(), mod, v1.UpdateOptions{})
		return err
	})
	requireAdmissionForbidden("update status of", func() error {
		_, err := userLCs.UpdateStatus(t.Context(), lc.DeepCopy(), v1.UpdateOptions{})
		return err
	})
	requireAdmissionForbidden("patch", func() error {
		_, err := userLCs.Patch(t.Context(), corev1alpha1.LogicalClusterName, types.MergePatchType, patch, v1.PatchOptions{})
		return err
	})
	requireAdmissionForbidden("delete", func() error {
		return userLCs.Delete(t.Context(), corev1alpha1.LogicalClusterName, v1.DeleteOptions{})
	})

	t.Log("Verify that the LogicalCluster is untouched")
	lc, err = adminKcpClient.Cluster(wsPath).CoreV1alpha1().LogicalClusters().Get(t.Context(), corev1alpha1.LogicalClusterName, v1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, lc.Annotations, annotationKey)
	require.Nil(t, lc.DeletionTimestamp)

	t.Log("Verify that kcp-admin can write the LogicalCluster")
	lc, err = adminKcpClient.Cluster(wsPath).CoreV1alpha1().LogicalClusters().Patch(t.Context(), corev1alpha1.LogicalClusterName, types.MergePatchType, patch, v1.PatchOptions{})
	require.NoError(t, err)
	require.Equal(t, "true", lc.Annotations[annotationKey])

	// System identities talk to the shard directly, so address the workspace by
	// its logical cluster name on the shard hosting it. Every group but
	// system:masters is obtained by impersonating it from system:masters.
	clusterPath := logicalcluster.Name(ws.Spec.Cluster).Path()
	shardCfg := server.ShardSystemMasterBaseConfig(t, kcptesting.WorkspaceShardOrDie(t, cfg, ws).Name)
	for _, group := range []string{
		bootstrap.SystemMastersGroup,
		bootstrap.SystemLogicalClusterAdmin,
		bootstrap.SystemExternalLogicalClusterAdmin,
		bootstrap.SystemKcpWorkspaceBootstrapper,
	} {
		t.Logf("Verify that %s can write the LogicalCluster", group)
		systemCfg := rest.CopyConfig(shardCfg)
		if group != bootstrap.SystemMastersGroup {
			systemCfg.Impersonate = rest.ImpersonationConfig{UserName: "e2e-" + group, Groups: []string{group}}
		}
		systemKcpClient, err := kcpclientset.NewForConfig(systemCfg)
		require.NoError(t, err)
		systemLCs := systemKcpClient.Cluster(clusterPath).CoreV1alpha1().LogicalClusters()

		// Update rather than patch: external-logical-cluster-admin is not granted patch.
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			lc, err := systemLCs.Get(t.Context(), corev1alpha1.LogicalClusterName, v1.GetOptions{})
			if err != nil {
				return err
			}
			if lc.Annotations == nil {
				lc.Annotations = map[string]string{}
			}
			lc.Annotations[annotationKey] = group
			_, err = systemLCs.Update(t.Context(), lc, v1.UpdateOptions{})
			return err
		})
		require.NoError(t, err, "%s should be able to update the LogicalCluster", group)

		lc, err = adminKcpClient.Cluster(wsPath).CoreV1alpha1().LogicalClusters().Get(t.Context(), corev1alpha1.LogicalClusterName, v1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, group, lc.Annotations[annotationKey])
	}
}
