/*
Copyright 2022 The kcp Authors.

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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestGarbageCollectorBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-builtins"))

	t.Logf("Creating owner configmap")
	owner, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(t.Context(),
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owner configmap %s|default/owner", wsPath)

	t.Logf("Creating owned configmap")
	owned, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(t.Context(),
		corev1ac.ConfigMap("owned", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").
				WithKind("ConfigMap").
				WithName(owner.Name).
				WithUID(owner.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owned configmap %s|default/owned", wsPath)

	t.Logf("Deleting owner configmap")
	err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Delete(t.Context(), owner.Name, metav1.DeleteOptions{})

	t.Logf("Waiting for the owned configmap to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Get(t.Context(), owned.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("configmap not garbage collected: %s", owned.Name)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmap to be garbage collected")
}

// TestGarbageCollectorDanglingOwnerRefCleanup verifies that when one of multiple
// owners is deleted, the GC patches the dangling ownerRef out of the dependent
// while leaving it alive with the surviving ownerRef intact.
func TestGarbageCollectorDanglingOwnerRefCleanup(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-dangling-ref-cleanup"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating two owner configmaps")
	ownerA, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-a", Namespace: "default"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	ownerB, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-b", Namespace: "default"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating child configmap owned by both owner-a and owner-b")
	_, err = cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "v1", Kind: "ConfigMap", Name: ownerA.Name, UID: ownerA.UID},
				{APIVersion: "v1", Kind: "ConfigMap", Name: ownerB.Name, UID: ownerB.UID},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Deleting owner-a")
	err = cmClient.Delete(t.Context(), "owner-a", metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for child to have exactly one ownerRef pointing to owner-b")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		child, err := cmClient.Get(t.Context(), "child", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting child: %v", err)
		}
		if len(child.OwnerReferences) != 1 {
			return false, fmt.Sprintf("expected 1 ownerRef, got %d: %v", len(child.OwnerReferences), child.OwnerReferences)
		}
		if child.OwnerReferences[0].UID != ownerB.UID {
			return false, fmt.Sprintf("expected remaining ownerRef UID %s, got %s", ownerB.UID, child.OwnerReferences[0].UID)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "dangling ownerRef was not cleaned up from child")
}

// TestGarbageCollectorDeepOwnershipChain verifies that transitive cascading deletion
// works: A owns B, B owns C. Deleting A causes B and C to be garbage collected.
func TestGarbageCollectorDeepOwnershipChain(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-deep-chain"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating chain: A -> B -> C")
	a, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("chain-a", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	b, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("chain-b", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").WithKind("ConfigMap").
				WithName(a.Name).WithUID(a.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	_, err = cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("chain-c", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").WithKind("ConfigMap").
				WithName(b.Name).WithUID(b.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Deleting A (root of chain)")
	err = cmClient.Delete(t.Context(), a.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for entire chain to be garbage collected")
	for _, name := range []string{"chain-a", "chain-b", "chain-c"} {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := cmClient.Get(t.Context(), name, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprintf("%s still exists", name)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, fmt.Sprintf("%s was not garbage collected", name))
	}
}

// TestGarbageCollectorMixedPoliciesInChain verifies the interaction of different
// deletion policies across an ownership chain: A -> B (with orphan finalizer) -> C.
// Deleting A cascades to B, but B's orphan finalizer causes C to be orphaned.
func TestGarbageCollectorMixedPoliciesInChain(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-mixed-policy"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating chain: A -> B (orphan finalizer) -> C")
	a, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("chain-a", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	b, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chain-b",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       a.Name,
				UID:        a.UID,
			}},
			Finalizers: []string{metav1.FinalizerOrphanDependents},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("chain-c", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").WithKind("ConfigMap").
				WithName(b.Name).WithUID(b.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Deleting A (root)")
	err = cmClient.Delete(t.Context(), a.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for A and B to be deleted")
	for _, name := range []string{"chain-a", "chain-b"} {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := cmClient.Get(t.Context(), name, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprintf("%s still exists", name)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, fmt.Sprintf("%s was not deleted", name))
	}

	t.Logf("Verifying C was orphaned (survives with empty ownerReferences)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		c, err := cmClient.Get(t.Context(), "chain-c", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting chain-c: %v", err)
		}
		if len(c.OwnerReferences) != 0 {
			return false, fmt.Sprintf("chain-c still has owner references: %v", c.OwnerReferences)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "chain-c was not orphaned correctly")
}

// TestGarbageCollectorDanglingOwnerRefGetsCollected verifies that the GC detects
// and collects objects whose ownerRefs point to non-existent UIDs, simulating
// objects that were orphaned before GC caught up and then re-assigned a stale ref.
func TestGarbageCollectorDanglingOwnerRefGetsCollected(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-dangling-ownerref-collected"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating owner and child configmaps")
	owner, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: "default"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	child, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "child",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Name:       owner.Name,
				UID:        owner.UID,
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Deleting owner with PropagationPolicy=Orphan")
	orphan := metav1.DeletePropagationOrphan
	err = cmClient.Delete(t.Context(), owner.Name, metav1.DeleteOptions{PropagationPolicy: &orphan})
	require.NoError(t, err)

	t.Logf("Waiting for child to be orphaned (ownerReferences cleared)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cm, err := cmClient.Get(t.Context(), child.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting child: %v", err)
		}
		if len(cm.OwnerReferences) != 0 {
			return false, fmt.Sprintf("child still has owner references: %v", cm.OwnerReferences)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "child was not orphaned")

	t.Logf("Patching child to re-add ownerRef pointing to deleted owner UID")
	patch, err := json.Marshal(map[string]interface{}{
		"metadata": map[string]interface{}{
			"ownerReferences": []interface{}{
				map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"name":       owner.Name,
					"uid":        string(owner.UID),
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = cmClient.Patch(t.Context(), child.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for child to be garbage collected (dangling UID)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), child.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "child configmap still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "child was not garbage collected after re-adding dangling ownerRef")
}

// TestGarbageCollectorUnknownOwnerType verifies that a namespaced object with
// an ownerRef pointing to a type that doesn't exist in discovery is preserved
// by the GC, because it cannot verify whether the owner exists.
func TestGarbageCollectorUnknownOwnerType(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-unknown-owner-type"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating configmap with unknown-type owner (should survive)")
	_, err = cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("orphan-with-unknown-type", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("nonexistent.example.com/v1").
				WithKind("Phantom").
				WithName("ghost").
				WithUID(types.UID("00000000-0000-0000-0000-000000000042"))),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "error creating configmap with unknown-type owner")

	t.Logf("Creating configmap with known-type but non-existent owner (should be GC'd)")
	_, err = cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("orphan-with-known-type", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").
				WithKind("ConfigMap").
				WithName("does-not-exist").
				WithUID(types.UID("00000000-0000-0000-0000-000000000099"))),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "error creating configmap with known-type owner")

	t.Logf("Waiting for orphan-with-known-type to be garbage collected (proves GC is active)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), "orphan-with-known-type", metav1.GetOptions{})
		return apierrors.IsNotFound(err), "orphan-with-known-type still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "orphan-with-known-type was not garbage collected")

	t.Logf("Verifying orphan-with-unknown-type survives")
	_, err = cmClient.Get(t.Context(), "orphan-with-unknown-type", metav1.GetOptions{})
	require.NoError(t, err, "orphan-with-unknown-type should survive (unknown type is unverifiable)")
}
