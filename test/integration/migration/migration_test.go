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

package migration

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

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/test/integration/framework"
)

var configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

// TestIgnoreCluster configures the informers to ignore all objects from
// a specific clusters and validates that no events related to that
// cluster are received by handlers and no objects from the cluster are
// listed.
func TestIgnoreCluster(t *testing.T) {
	t.Parallel()

	server, kcpClusterClient, kubeClusterClient := framework.StartTestServer(t)

	ws := kcptesting.NewLowLevelWorkspaceFixture[kcptesting.UnprivilegedWorkspaceOption](t, kcpClusterClient, kcpClusterClient, core.RootCluster.Path())
	clusterName := logicalcluster.Name(ws.Spec.Cluster)
	wsPath := core.RootCluster.Path().Join(ws.Name)

	t.Log("Create ConfigMap")
	preConfigMap, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
		t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "pre-ignore-"},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap")

	ddsif := server.Server.PartialMetadataDDSIF

	t.Log("Waiting for configmap to be listed and hence stored in the cache")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		informers, _ := ddsif.Informers()
		inf := informers[configMapGVR]
		require.NotNil(c, inf)

		items, err := inf.Lister().List(labels.Everything())
		require.NoError(c, err)
		require.Greater(c, len(items), 0)

		found := false
		for _, item := range items {
			accessor, err := meta.Accessor(item)
			require.NoError(c, err)
			if accessor.GetName() != preConfigMap.Name {
				continue
			}
			found = true
			require.Equal(c, clusterName, logicalcluster.From(accessor))
		}
		require.True(c, found)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Register handler to ensure no events for the target logical cluster come through")
	ddsif.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			accessor, err := meta.Accessor(obj)
			if !assert.NoError(t, err, "unable to get metav1.Object from %q", obj) {
				return
			}
			assert.False(t, logicalcluster.From(accessor) == clusterName, "received create event for object %q from excluded cluster %q", obj, clusterName)
		},
		UpdateFunc: func(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
			oldAccessor, err := meta.Accessor(oldObj)
			if !assert.NoError(t, err, "unable to get metav1.Object from %q", oldAccessor) {
				return
			}
			assert.False(t, logicalcluster.From(oldAccessor) == clusterName, "received update event for object %q from excluded cluster %q", oldObj, clusterName)
			newAccessor, err := meta.Accessor(newObj)
			if !assert.NoError(t, err, "unable to get metav1.Object from %q", newAccessor) {
				return
			}
			assert.False(t, logicalcluster.From(newAccessor) == clusterName, "received update event for object %q from excluded cluster %q", newObj, clusterName)
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			accessor, err := meta.Accessor(obj)
			if !assert.NoError(t, err, "unable to get metav1.Object from %q", obj) {
				return
			}
			assert.False(t, logicalcluster.From(accessor) == clusterName, "received delete event for object %q from excluded cluster %q", obj, clusterName)
		},
	})

	t.Logf("Configure ddsif to ignore cluster %q", clusterName)
	ddsif.IgnoreCluster(clusterName)

	t.Log("Create second ConfigMap")
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
		t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "post-ignore-"},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap")

	t.Log("Verifying configmaps from the ignored lc are not longer visible in lister")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		informers, _ := ddsif.Informers()
		inf := informers[configMapGVR]
		require.NotNil(c, inf)

		items, err := inf.Lister().List(labels.Everything())
		require.NoError(c, err)

		found := false
		for _, item := range items {
			accessor, err := meta.Accessor(item)
			require.NoError(c, err)
			require.NotEqual(c, clusterName, logicalcluster.From(accessor))
		}
		require.False(c, found)
	}, 5*time.Second, 100*time.Millisecond)

	t.Log("Wait a bit for (hopefully no) events to arrive at the handler")
	time.Sleep(5 * time.Second)
}
