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

package garbagecollector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestGarbageCollectorVersionedCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	group := framework.UniqueGroup(".io")

	sheriffCRD := apifixtures.NewSheriffsCRDWithVersions(group, "v1", "v2")

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-crd-versions"))

	t.Logf("Install a versioned sheriffs CRD into workspace %q", wsPath)
	bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD)

	sheriffsGVRv1 := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	sheriffsGVRv2 := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v2"}

	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(wsPath).Discovery(), sheriffsGVRv1.GroupVersion())

	t.Logf("Creating owner v1 sheriff")
	ownerv1, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owner-v1",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner sheriff %s|default/owner-v1", wsPath)

	t.Logf("Creating owned v1 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v1",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv1.GetAPIVersion(),
							"kind":       ownerv1.GetKind(),
							"name":       ownerv1.GetName(),
							"uid":        ownerv1.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v1", wsPath)

	t.Logf("Creating owned v2 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v2",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv1.GetAPIVersion(),
							"kind":       ownerv1.GetKind(),
							"name":       ownerv1.GetName(),
							"uid":        ownerv1.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v2", wsPath)

	t.Logf("Deleting owner v1 sheriff")
	err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Delete(t.Context(), ownerv1.GetName(), metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting sheriff %s in %s", ownerv1.GetName(), wsPath)

	t.Logf("Waiting for the owned sheriffs to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err1 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").Get(t.Context(), "owned-v1", metav1.GetOptions{})
		_, err2 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").Get(t.Context(), "owned-v2", metav1.GetOptions{})
		return apierrors.IsNotFound(err1) && apierrors.IsNotFound(err2), "sheriffs not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned sheriffs to be garbage collected")

	t.Logf("Creating owner v2 sheriff")
	ownerv2, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owner-v2",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner sheriff %s|default/owner-v2", wsPath)

	t.Logf("Creating owned v1 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v1",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv2.GetAPIVersion(),
							"kind":       ownerv2.GetKind(),
							"name":       ownerv2.GetName(),
							"uid":        ownerv2.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v1", wsPath)

	t.Logf("Creating owned v2 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v2",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv2.GetAPIVersion(),
							"kind":       ownerv2.GetKind(),
							"name":       ownerv2.GetName(),
							"uid":        ownerv2.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v2", wsPath)

	t.Logf("Deleting owner v2 sheriff")
	err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Delete(t.Context(), ownerv2.GetName(), metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting sheriff %s in %s", ownerv2.GetName(), wsPath)

	t.Logf("Waiting for the owned sheriffs to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err1 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").Get(t.Context(), "owned-v1", metav1.GetOptions{})
		_, err2 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").Get(t.Context(), "owned-v2", metav1.GetOptions{})
		return apierrors.IsNotFound(err1) && apierrors.IsNotFound(err2), "sheriffs not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned sheriffs to be garbage collected")
}

func TestGarbageCollectorClusterScopedCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	group := framework.UniqueGroup(".io")

	crd := newClusterScopedCRD(group, "clustered")

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-crd-cluster-scope"))

	t.Logf("Install cluster-scoped CRD into workspace %q", wsPath)
	bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), crd)

	gvr := schema.GroupVersionResource{Group: group, Resource: crd.Spec.Names.Plural, Version: "v1"}

	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(wsPath).Discovery(), gvr.GroupVersion())

	t.Logf("Creating owner clustered")
	owner, err := dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Create(t.Context(), &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": gvr.GroupVersion().String(),
				"kind":       crd.Spec.Names.Kind,
				"metadata": map[string]interface{}{
					"name": "owner",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner clustered %s|default/owner", wsPath)

	t.Logf("Creating owned clustered")
	owned := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": gvr.GroupVersion().String(),
			"kind":       crd.Spec.Names.Kind,
			"metadata": map[string]interface{}{
				"name": "owned",
			},
		},
	}
	owned.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: gvr.GroupVersion().String(),
			Kind:       owner.GetKind(),
			Name:       owner.GetName(),
			UID:        owner.GetUID(),
		},
	})
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Create(t.Context(), owned, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned clustered %s|default/owned", wsPath)

	t.Logf("Deleting owner clustered")
	err = dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Delete(t.Context(), "owner", metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting owner clustered in %s", wsPath)

	t.Logf("Waiting for the owned clustered to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.Cluster(wsPath).Resource(gvr).
			Get(t.Context(), "owned", metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owned clustered not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned clustered to be garbage collected")
}

func TestGarbageCollectorNormalCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	group := framework.UniqueGroup(".io")

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	ws1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-crd-1"))
	ws2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-crd-2"))

	t.Logf("Install a normal sheriffs CRD into workspace 1 %q", ws1Path)
	bootstrapCRD(t, ws1Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD with a different schema into workspace 2 %q", ws2Path)
	bootstrapCRD(t, ws2Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(ws1Path).Discovery(), sheriffsGVR.GroupVersion())
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(ws2Path).Discovery(), sheriffsGVR.GroupVersion())

	// Test with 2 workspaces to make sure GC works for both
	workspaces := []logicalcluster.Path{ws1Path, ws2Path}
	for _, wsPath := range workspaces {
		t.Logf("Creating owner sheriff")
		owner, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVR).Namespace("default").
			Create(t.Context(), &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": sheriffsGVR.GroupVersion().String(),
					"kind":       "Sheriff",
					"metadata": map[string]interface{}{
						"name": "owner",
					},
				},
			}, metav1.CreateOptions{})
		require.NoError(t, err, "Error creating owner sheriff %s|default/owner", wsPath)

		t.Logf("Creating owned configmap")
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").
			Apply(t.Context(), corev1ac.ConfigMap("owned", "default").
				WithOwnerReferences(metav1ac.OwnerReference().
					WithAPIVersion(sheriffsGVR.GroupVersion().String()).
					WithKind(owner.GetKind()).
					WithName(owner.GetName()).
					WithUID(owner.GetUID())),
				metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
		require.NoError(t, err, "Error applying owned configmap %s|default/owned", wsPath)
	}

	t.Logf("Deleting all sheriffs")
	for _, ws := range workspaces {
		err = dynamicClusterClient.Cluster(ws).Resource(sheriffsGVR).Namespace("default").
			DeleteCollection(t.Context(), metav1.DeleteOptions{}, metav1.ListOptions{})
		require.NoError(t, err, "Error deleting all sheriffs in %s", ws)
	}

	t.Logf("Waiting for the owned configmaps in ws1 to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err1 := kubeClusterClient.Cluster(ws1Path).CoreV1().ConfigMaps("default").Get(t.Context(), "owned", metav1.GetOptions{})
		return apierrors.IsNotFound(err1), "configmaps not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmaps ws1 to be garbage collected")

	t.Logf("Waiting for the owned configmaps in ws2 to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err2 := kubeClusterClient.Cluster(ws2Path).CoreV1().ConfigMaps("default").Get(t.Context(), "owned", metav1.GetOptions{})
		return apierrors.IsNotFound(err2), "configmaps not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmaps ws2 to be garbage collected")
}
