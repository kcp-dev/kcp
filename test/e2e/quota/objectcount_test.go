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

package quota

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestLogicalClusterTotalObjectCountLimit exercises the poor man's quota: a
// hard limit on the total number of objects in a logical cluster, configured
// via the core.kcp.io/max-total-objects annotation on the LogicalCluster.
func TestLogicalClusterTotalObjectCountLimit(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// A private server is used because the object count scanner needs a fast
	// scan interval for the test to be quick. No shard-wide default limit is
	// set (it would also apply to the root workspace); enforcement is enabled
	// per logical cluster via the annotation.
	server := kcptesting.PrivateKcpServer(t,
		kcptestingserver.WithCustomArguments("--logical-cluster-object-count-scan-interval=1s"),
	)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	const limit = 40

	t.Logf("Setting the max total objects annotation to %d on the LogicalCluster of %q", limit, wsPath)
	setMaxTotalObjectsAnnotation(t, kcpClusterClient, wsPath, fmt.Sprintf("%d", limit))

	t.Logf("Creating ConfigMaps in %q until the total object count limit is reached", wsPath)
	created := make([]string, 0, limit)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		name := fmt.Sprintf("quota-test-%d", len(created))
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}, metav1.CreateOptions{})
		switch {
		case err == nil:
			created = append(created, name)
			if len(created) > 2*limit {
				t.Fatalf("created %d ConfigMaps without hitting the limit of %d", len(created), limit)
			}
			return false, fmt.Sprintf("created %d ConfigMaps so far, limit not reached yet", len(created))
		case apierrors.IsForbidden(err):
			require.Contains(t, err.Error(), "total object count limit", "unexpected forbidden error")
			return true, ""
		default:
			return false, err.Error()
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected creates to eventually be forbidden")
	require.NotEmpty(t, created, "expected at least one ConfigMap to be created before hitting the limit")

	t.Logf("Deleting one ConfigMap to free up capacity")
	err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Delete(t.Context(), created[0], metav1.DeleteOptions{})
	require.NoError(t, err, "deletes must always be allowed")

	t.Logf("Verifying that one create slot is free again")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "quota-test-refill"},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected create to succeed after delete freed capacity")

	t.Logf("Verifying that creates are rejected again once the limit is reached")
	rejected := 0
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("quota-test-again-%d", rejected)},
		}, metav1.CreateOptions{})
		switch {
		case err == nil:
			rejected++
			return false, "create still succeeding"
		case apierrors.IsForbidden(err):
			return true, ""
		default:
			return false, err.Error()
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected creates to eventually be forbidden again")

	t.Logf("Opting the logical cluster out via annotation value 0")
	setMaxTotalObjectsAnnotation(t, kcpClusterClient, wsPath, "0")

	t.Logf("Verifying that creates succeed again")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "quota-test-unlimited"},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected creates to succeed after opting out")

	t.Logf("Verifying that other logical clusters are not affected")
	otherPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	_, err = kubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-test-other"},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "expected create in another workspace to succeed")
}

// TestLogicalClusterTotalObjectCountLimitShardDefault exercises the shard-wide
// default limit set via the --logical-cluster-total-object-limit flag: a fresh
// workspace without any annotation must inherit the default, while the root
// logical cluster is exempted from the default by design (the shard writes
// bootstrap content there, and a default limit must never break that).
func TestLogicalClusterTotalObjectCountLimitShardDefault(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const limit = 60

	// The server starting up at all already proves the root exemption: root
	// contains far more bootstrap objects than the test limit.
	server := kcptesting.PrivateKcpServer(t,
		kcptestingserver.WithCustomArguments(
			fmt.Sprintf("--logical-cluster-total-object-limit=%d", limit),
			"--logical-cluster-object-count-scan-interval=1s",
		),
	)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

	t.Logf("Creating ConfigMaps in %q (no annotation) until the shard-wide default limit is reached", wsPath)
	created := 0
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("quota-default-test-%d", created)},
		}, metav1.CreateOptions{})
		switch {
		case err == nil:
			created++
			if created > 2*limit {
				t.Fatalf("created %d ConfigMaps without hitting the shard-wide default limit of %d", created, limit)
			}
			return false, fmt.Sprintf("created %d ConfigMaps so far, limit not reached yet", created)
		case apierrors.IsForbidden(err):
			require.Contains(t, err.Error(), "total object count limit", "unexpected forbidden error")
			return true, ""
		default:
			return false, err.Error()
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected creates to eventually be forbidden by the shard-wide default limit")
	require.NotZero(t, created, "expected at least one ConfigMap to be created before hitting the limit")

	t.Logf("Verifying that root is exempted: creates into root must still succeed")
	_, err = kubeClusterClient.Cluster(core.RootCluster.Path()).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "quota-default-test-root"},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "expected create in exempted root workspace to succeed")

	t.Logf("Verifying that a per-workspace annotation overrides the shard-wide default")
	setMaxTotalObjectsAnnotation(t, kcpClusterClient, wsPath, fmt.Sprintf("%d", 2*limit))
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "quota-default-test-raised"},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected create to succeed after raising the limit via annotation")
}

func setMaxTotalObjectsAnnotation(t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, path logicalcluster.Path, value string) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		lc, err := kcpClusterClient.Cluster(path).CoreV1alpha1().LogicalClusters().Get(t.Context(), corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if lc.Annotations == nil {
			lc.Annotations = map[string]string{}
		}
		lc.Annotations[corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey] = value
		if _, err := kcpClusterClient.Cluster(path).CoreV1alpha1().LogicalClusters().Update(t.Context(), lc, metav1.UpdateOptions{}); err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to set %s=%s", corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey, value)
}
