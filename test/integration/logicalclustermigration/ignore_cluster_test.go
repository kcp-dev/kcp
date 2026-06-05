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

package logicalclustermigration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/integration/framework"
)

// TestIgnoreCluster tests that adding a cluster to the migrating
// logical clusters causes all objects from that cluster to be ignored
// and inaccessible for kcp reconcilers.
func TestIgnoreCluster(t *testing.T) {
	t.Parallel()

	server, kcpClientSet, kubeClient := framework.StartTestServer(t)

	workspace := kcptesting.NewLowLevelWorkspaceFixture(t, kcpClientSet, kcpClientSet, core.RootCluster.Path(), kcptesting.WithNamePrefix("ignore-cluster"))
	lcName := logicalcluster.Name(workspace.Spec.Cluster)

	t.Logf("Creating ConfigMap in workspace %s", lcName)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cm"},
		Data:       map[string]string{"key": "value"},
	}

	_, err := kubeClient.Cluster(lcName.Path()).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	ddsif := server.Server.PartialMetadataDDSIF
	configMapGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

	t.Logf("Verifying ConfigMap is visible in DDSIF store")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		inf, err := ddsif.ForResource(configMapGVR)
		require.NoError(c, err)
		objs, err := inf.Lister().ByCluster(lcName).List(labels.Everything())
		require.NoError(c, err)
		require.NotEmpty(c, objs, "no configmaps found in DDSIF store")
		found := false
		for _, obj := range objs {
			accessor, err := meta.Accessor(obj)
			require.NoError(c, err)
			if accessor.GetName() == cm.Name {
				found = true
			}
		}
		require.True(c, found, "configmap %q not found in DDSIF store", cm.Name)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Marking logical cluster %s as migrating and purging from DDSIF", lcName)
	err = server.Server.MigratingLogicalClusters.Set(lcName, "test-migration")
	require.NoError(t, err)
	ddsif.PurgeCluster(lcName)

	t.Logf("Verifying ConfigMap is no longer visible in DDSIF store")
	inf, err := ddsif.ForResource(configMapGVR)
	require.NoError(t, err)

	objs, err := inf.Lister().ByCluster(lcName).List(labels.Everything())
	require.NoError(t, err)
	require.Empty(t, objs)

	t.Logf("Removing logical cluster %s from migration and forcing relist", lcName)
	server.Server.MigratingLogicalClusters.Remove(lcName)
	ddsif.ForceRelist()

	t.Logf("Verifying ConfigMap reappears in DDSIF store after relist")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		objs, err := inf.Lister().ByCluster(lcName).List(labels.Everything())
		require.NoError(c, err)
		require.NotEmpty(c, objs, "configmap not yet back in DDSIF store")
		found := false
		for _, obj := range objs {
			accessor, err := meta.Accessor(obj)
			require.NoError(c, err)
			if accessor.GetName() == cm.Name {
				found = true
			}
		}
		require.True(c, found, "configmap %q not found in DDSIF store", cm.Name)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
