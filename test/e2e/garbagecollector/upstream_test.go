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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Tests in this file are roughly equivalent to the upstream GC integration tests:
// https://github.com/kubernetes/kubernetes/blob/a3895062546711ae2666848a7930ae3b4ffae220/test/integration/garbagecollector/garbage_collector_test.go
// They exist to ensure that the GC in a workspace still acts like an
// end user would expect from the normal kubernetes API.

// TestCascadingDeletion verifies that when a resource has multiple
// owners, it is only garbage collected when all owners are deleted. It
// also verifies that resources without owners are not affected by
// cascading deletion.
func TestCascadingDeletion(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-cascading"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating two owner configmaps")
	owner1, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner-1", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	owner2, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner-2", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating configmap owned only by owner-1 (should be cascading deleted)")
	singleOwned, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("single-owned", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").WithKind("ConfigMap").
				WithName(owner1.Name).WithUID(owner1.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating configmap owned by both owners (should survive deletion of one)")
	multiOwned, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("multi-owned", "default").
			WithOwnerReferences(
				metav1ac.OwnerReference().
					WithAPIVersion("v1").WithKind("ConfigMap").
					WithName(owner1.Name).WithUID(owner1.UID),
				metav1ac.OwnerReference().
					WithAPIVersion("v1").WithKind("ConfigMap").
					WithName(owner2.Name).WithUID(owner2.UID),
			),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating independent configmap without owners (should not be affected)")
	independent, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("independent", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Deleting owner-1")
	err = cmClient.Delete(t.Context(), owner1.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for single-owned configmap to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), singleOwned.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "single-owned configmap not yet deleted"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "single-owned configmap was not garbage collected")

	t.Logf("Verifying multi-owned configmap still exists")
	_, err = cmClient.Get(t.Context(), multiOwned.Name, metav1.GetOptions{})
	require.NoError(t, err, "multi-owned configmap should still exist")

	t.Logf("Verifying independent configmap still exists")
	_, err = cmClient.Get(t.Context(), independent.Name, metav1.GetOptions{})
	require.NoError(t, err, "independent configmap should still exist")

	t.Logf("Deleting owner-2")
	err = cmClient.Delete(t.Context(), owner2.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for multi-owned configmap to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), multiOwned.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "multi-owned configmap not yet deleted"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "multi-owned configmap was not garbage collected")

	t.Logf("Verifying independent configmap is still untouched")
	_, err = cmClient.Get(t.Context(), independent.Name, metav1.GetOptions{})
	require.NoError(t, err, "independent configmap should still exist")
}

// TestCreateWithNonExistentOwner verifies that a resource created with
// an owner reference pointing to a non-existent UID gets garbage
// collected.
func TestCreateWithNonExistentOwner(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-nonexistent-owner"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating configmap with a non-existent owner reference")
	orphan, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("orphan", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").WithKind("ConfigMap").
				WithName("does-not-exist").WithUID("nonexistent-uid")),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Waiting for the orphan configmap to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), orphan.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "orphan configmap not yet deleted"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "orphan configmap was not garbage collected")
}

// TestOrphaning verifies that deleting an owner with
// PropagationPolicy=Orphan causes dependents to survive with their
// owner references removed.
func TestOrphaning(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-orphaning"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating owner configmap")
	owner, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating dependent configmaps")
	dependentNames := make([]string, 0, 3)
	for i := range 3 {
		name := fmt.Sprintf("dependent-%d", i)
		_, err := cmClient.Apply(t.Context(),
			corev1ac.ConfigMap(name, "default").
				WithOwnerReferences(metav1ac.OwnerReference().
					WithAPIVersion("v1").WithKind("ConfigMap").
					WithName(owner.Name).WithUID(owner.UID)),
			metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
		require.NoError(t, err)
		dependentNames = append(dependentNames, name)
	}

	t.Logf("Deleting owner with PropagationPolicy=Orphan")
	orphanPolicy := metav1.DeletePropagationOrphan
	err = cmClient.Delete(t.Context(), owner.Name, metav1.DeleteOptions{PropagationPolicy: &orphanPolicy})
	require.NoError(t, err)

	t.Logf("Waiting for owner to be deleted")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), owner.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owner configmap still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "owner configmap was not deleted")

	t.Logf("Verifying dependents still exist and have empty owner references")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		for _, name := range dependentNames {
			cm, err := cmClient.Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				return false, fmt.Sprintf("error getting dependent %s: %v", name, err)
			}
			if len(cm.OwnerReferences) != 0 {
				return false, fmt.Sprintf("dependent %s still has owner references: %v", name, cm.OwnerReferences)
			}
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "dependents were not orphaned correctly")
}

// TestSolidOwnerDoesNotBlockWaitingOwner verifies foreground deletion
// with BlockOwnerDeletion. When a dependent has two owners, one deleted
// with foreground policy and BlockOwnerDeletion=true, and the other
// still alive, the foreground-deleted owner should still be removed and
// the dependent should keep only the surviving owner reference.
func TestSolidOwnerDoesNotBlockWaitingOwner(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-foreground"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	secretClient := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default")

	t.Logf("Creating two owner configmaps")
	ownerToDelete, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner-to-delete", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	ownerToKeep, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner-to-keep", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating dependent secret with BlockOwnerDeletion for the to-be-deleted owner")
	blockOwnerDeletion := true
	dependent, err := secretClient.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dependent",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "ConfigMap",
					Name:               ownerToDelete.Name,
					UID:                ownerToDelete.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
				{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Name:       ownerToKeep.Name,
					UID:        ownerToKeep.UID,
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Deleting owner-to-delete with foreground propagation")
	foreground := metav1.DeletePropagationForeground
	err = cmClient.Delete(t.Context(), ownerToDelete.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for owner-to-delete to be fully removed")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), ownerToDelete.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owner-to-delete still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "owner-to-delete was not deleted")

	t.Logf("Verifying dependent still exists and only references the surviving owner")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		s, err := secretClient.Get(t.Context(), dependent.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting dependent: %v", err)
		}
		if len(s.OwnerReferences) != 1 {
			return false, fmt.Sprintf("expected 1 owner reference, got %d: %v", len(s.OwnerReferences), s.OwnerReferences)
		}
		if s.OwnerReferences[0].Name != ownerToKeep.Name {
			return false, fmt.Sprintf("expected remaining owner to be %s, got %s", ownerToKeep.Name, s.OwnerReferences[0].Name)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "dependent owner references were not updated correctly")
}

// TestNonBlockingOwnerRefDoesNotBlock verifies that foreground deletion
// of an owner proceeds even when dependents cannot be deleted (due to
// finalizers), as long as the dependents do not set
// BlockOwnerDeletion=true.
func TestGarbageCollectorNonBlockingOwnerRefDoesNotBlock(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-nonblocking"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	secretClient := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default")

	t.Logf("Creating owner configmap")
	owner, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating dependent secret without BlockOwnerDeletion but with a finalizer")
	blockOwnerDeletion := false
	_, err = secretClient.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dependent-no-block",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "ConfigMap",
					Name:               owner.Name,
					UID:                owner.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
			Finalizers: []string{"e2e-test/prevent-deletion"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating dependent secret without BlockOwnerDeletion set at all, with a finalizer")
	_, err = secretClient.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dependent-unset",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Name:       owner.Name,
					UID:        owner.UID,
				},
			},
			Finalizers: []string{"e2e-test/prevent-deletion"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Deleting owner with foreground propagation")
	foreground := metav1.DeletePropagationForeground
	err = cmClient.Delete(t.Context(), owner.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for owner to be deleted despite dependents having finalizers")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), owner.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owner configmap still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "owner was not deleted")

	t.Logf("Verifying both dependents still exist (blocked by finalizers)")
	_, err = secretClient.Get(t.Context(), "dependent-no-block", metav1.GetOptions{})
	require.NoError(t, err, "dependent-no-block should still exist")
	_, err = secretClient.Get(t.Context(), "dependent-unset", metav1.GetOptions{})
	require.NoError(t, err, "dependent-unset should still exist")
}

// TestBlockingOwnerRefDoesBlock verifies that foreground deletion of an
// owner is blocked when a dependent has BlockOwnerDeletion=true and
// a finalizer preventing its own deletion.
func TestGarbageCollectorBlockingOwnerRefDoesBlock(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-blocking"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	secretClient := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default")

	t.Logf("Creating owner configmap")
	owner, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Creating dependent secret with BlockOwnerDeletion=true and a finalizer")
	blockOwnerDeletion := true
	dependent, err := secretClient.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "blocking-dependent",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "ConfigMap",
					Name:               owner.Name,
					UID:                owner.UID,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
			Finalizers: []string{"e2e-test/prevent-deletion"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Deleting owner with foreground propagation")
	foreground := metav1.DeletePropagationForeground
	err = cmClient.Delete(t.Context(), owner.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for owner to enter deletion")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := cmClient.Get(t.Context(), owner.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting owner: %v", err)
		}
		if obj.DeletionTimestamp == nil {
			return false, "owner has no deletionTimestamp yet"
		}
		for _, f := range obj.Finalizers {
			if f == metav1.FinalizerDeleteDependents {
				return true, ""
			}
		}
		return false, fmt.Sprintf("owner has deletionTimestamp but no foregroundDeletion finalizer, finalizers: %v", obj.Finalizers)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "owner did not enter foreground deletion state")

	t.Logf("Verifying dependent still exists (blocks owner deletion)")
	_, err = secretClient.Get(t.Context(), dependent.Name, metav1.GetOptions{})
	require.NoError(t, err, "dependent should still exist due to finalizer")

	_, err = cmClient.Get(t.Context(), owner.Name, metav1.GetOptions{})
	require.NoError(t, err, "owner should still exist because dependent blocks its deletion")

	_, err = secretClient.Get(t.Context(), dependent.Name, metav1.GetOptions{})
	require.NoError(t, err, "dependent should still exist due to finalizer")

	t.Logf("Removing finalizer from dependent to unblock owner deletion")
	_, err = secretClient.Patch(t.Context(), dependent.Name, types.JSONPatchType,
		[]byte(`[{"op":"remove","path":"/metadata/finalizers"}]`),
		metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for owner to be deleted now that dependent can be removed")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), owner.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owner configmap still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "owner was not deleted after removing dependent finalizer")

	t.Logf("Verifying dependent was also garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := secretClient.Get(t.Context(), dependent.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "dependent secret still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "dependent was not garbage collected")
}

// TestDoubleDeletionWithFinalizer verifies that foreground-deleting an
// object twice does not cause the GC to re-add its finalizer, and that
// removing the custom finalizer allows the object to be deleted.
func TestDoubleDeletionWithFinalizer(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-double-foreground"))

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")

	t.Logf("Creating configmap with a custom finalizer")
	cm, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "double-delete",
			Namespace:  "default",
			Finalizers: []string{"e2e-test/custom"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("First foreground delete")
	foreground := metav1.DeletePropagationForeground
	err = cmClient.Delete(t.Context(), cm.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for GC to remove foregroundDeletion finalizer (only custom remains)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := cmClient.Get(t.Context(), cm.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting configmap: %v", err)
		}
		if len(obj.Finalizers) != 1 || obj.Finalizers[0] != "e2e-test/custom" {
			return false, fmt.Sprintf("expected only custom finalizer, got %v", obj.Finalizers)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "GC did not remove foregroundDeletion finalizer")

	t.Logf("Second foreground delete")
	err = cmClient.Delete(t.Context(), cm.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Verifying only custom finalizer remains after second delete")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := cmClient.Get(t.Context(), cm.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting configmap: %v", err)
		}
		if len(obj.Finalizers) != 1 || obj.Finalizers[0] != "e2e-test/custom" {
			return false, fmt.Sprintf("expected only custom finalizer, got %v", obj.Finalizers)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "GC re-added foregroundDeletion finalizer on second delete")

	t.Logf("Removing custom finalizer")
	_, err = cmClient.Patch(t.Context(), cm.Name, types.JSONPatchType,
		[]byte(`[{"op":"remove","path":"/metadata/finalizers"}]`),
		metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for configmap to be fully deleted")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), cm.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "configmap still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "configmap was not deleted after removing finalizer")
}

// TestMixedRelationships verifies that GC properly cascades deletions
// across custom resource and core resource type boundaries.
func TestMixedRelationships(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-mixed-rel"))

	group := framework.UniqueGroup(".io")
	sheriffCRD := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "mixed")
	bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(wsPath).Discovery(), sheriffsGVR.GroupVersion())

	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	sheriffClient := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVR).Namespace("default")

	t.Logf("Creating custom owner (Sheriff) with core dependent (ConfigMap)")
	customOwner, err := sheriffClient.Create(t.Context(), &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sheriffsGVR.GroupVersion().String(),
			"kind":       "Sheriff",
			"metadata":   map[string]interface{}{"name": "custom-owner"},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	coreDependent, err := cmClient.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "core-dependent",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: sheriffsGVR.GroupVersion().String(),
				Kind:       "Sheriff",
				Name:       customOwner.GetName(),
				UID:        customOwner.GetUID(),
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating core owner (ConfigMap) with custom dependent (Sheriff)")
	coreOwner, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("core-owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	customDependent, err := sheriffClient.Create(t.Context(), &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sheriffsGVR.GroupVersion().String(),
			"kind":       "Sheriff",
			"metadata": map[string]interface{}{
				"name": "custom-dependent",
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "ConfigMap",
						"name":       coreOwner.Name,
						"uid":        string(coreOwner.UID),
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Foreground-deleting custom owner")
	foreground := metav1.DeletePropagationForeground
	err = sheriffClient.Delete(t.Context(), customOwner.GetName(), metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for custom owner and core dependent to be deleted")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := sheriffClient.Get(t.Context(), customOwner.GetName(), metav1.GetOptions{})
		return apierrors.IsNotFound(err), "custom owner still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "custom owner was not deleted")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), coreDependent.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "core dependent still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "core dependent was not deleted")

	t.Logf("Foreground-deleting core owner")
	err = cmClient.Delete(t.Context(), coreOwner.Name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	require.NoError(t, err)

	t.Logf("Waiting for core owner and custom dependent to be deleted")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), coreOwner.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "core owner still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "core owner was not deleted")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := sheriffClient.Get(t.Context(), customDependent.GetName(), metav1.GetOptions{})
		return apierrors.IsNotFound(err), "custom dependent still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "custom dependent was not deleted")
}

// TestCrossNamespaceReferences verifies that invalid cross-namespace
// owner references (where the UID matches an object in a different
// namespace with a different Kind) are detected and the dependent is
// garbage collected, while valid same-namespace references are not
// affected.
//
// Upstream: TestCrossNamespaceReferences{WithWatchCache,WithoutWatchCache}.
func TestCrossNamespaceReferences(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-cross-ns"))

	nsClient := kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces()

	t.Logf("Creating namespaces")
	_, err = nsClient.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-b"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = nsClient.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns-a"}}, metav1.CreateOptions{})
	require.NoError(t, err)

	cmClientB := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("ns-b")
	secretClientB := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("ns-b")
	secretClientA := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("ns-a")

	t.Logf("Creating parent configmap in ns-b")
	parent, err := cmClientB.Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "parent"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating 10 valid children in ns-b")
	validChildCount := 10
	for i := range validChildCount {
		_, err := secretClientB.Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("valid-child-%d", i),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Name:       parent.Name,
					UID:        parent.UID,
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Logf("Creating invalid children in ns-a (cross-namespace ref with wrong Kind)")
	invalidCount := 5
	for i := range invalidCount {
		_, err := secretClientA.Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("invalid-child-%d", i),
				Labels: map[string]string{"invalid": "true"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1",
					Kind:       "Secret", // wrong kind — parent is a ConfigMap
					Name:       "invalid",
					UID:        parent.UID,
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Logf("Waiting for invalid children in ns-a to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := secretClientA.List(t.Context(), metav1.ListOptions{LabelSelector: "invalid=true"})
		if err != nil {
			return false, fmt.Sprintf("error listing: %v", err)
		}
		if len(list.Items) > 0 {
			return false, fmt.Sprintf("%d invalid children remain", len(list.Items))
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "invalid children were not garbage collected")

	t.Logf("Verifying OwnerRefInvalidNamespace warning events were recorded")
	eventClient := kubeClusterClient.Cluster(wsPath).CoreV1().Events("ns-a")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		events, err := eventClient.List(t.Context(), metav1.ListOptions{
			FieldSelector: "reason=OwnerRefInvalidNamespace",
		})
		require.NoError(c, err)
		require.NotEmpty(c, events.Items, "no OwnerRefInvalidNamespace events found yet")
		for _, event := range events.Items {
			require.Equal(c, "Warning", event.Type, "event type")
			require.Equal(c, "OwnerRefInvalidNamespace", event.Reason, "event reason")
			require.Equal(c, "garbage-collector-controller", event.Source.Component, "event source component")
			require.Equal(c, "Secret", event.InvolvedObject.Kind, "involved object kind")
			require.Contains(c, event.InvolvedObject.Name, "invalid-child-", "invalid object name")
			require.Equal(c, "v1", event.InvolvedObject.APIVersion, "involved object apiVersion")
			require.Equal(c, "ns-a", event.InvolvedObject.Namespace, "involved object namespace")
			require.Contains(c, event.Message, "does not exist in namespace", "event message")
			require.Contains(c, event.Message, "ns-a", "event message should reference the dependent namespace")
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Verifying valid children in ns-b still exist")
	list, err := secretClientB.List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, validChildCount, "expected %d valid children, got %d", validChildCount, len(list.Items))

	t.Logf("Creating new invalid child after GC sync to verify it's also collected")
	newInvalid, err := secretClientA.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "invalid-child-late",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Secret",
				Name:       "invalid",
				UID:        parent.UID,
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := secretClientA.Get(t.Context(), newInvalid.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "late invalid child still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "late invalid child was not garbage collected")
}

// TestCRDDeletionCascading verifies that deleting a CRD itself cascades
// to all instances, and those instances' dependents (including core
// resources) are also garbage collected.
func TestGarbageCollectorCRDDeletionCascading(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-crd-delete-cascade"))

	group := framework.UniqueGroup(".io")
	sheriffCRD := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "cascade")
	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	crdClient := crdClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	t.Logf("Installing Sheriff CRD")
	bootstrapCRD(t, wsPath, crdClient, sheriffCRD)
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(wsPath).Discovery(), sheriffsGVR.GroupVersion())

	sheriffClient := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVR).Namespace("default")

	t.Logf("Creating Sheriff owner instance")
	owner, err := sheriffClient.Create(t.Context(),
		&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": sheriffsGVR.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata":   map[string]any{"name": "owner"},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Creating ConfigMap dependent owned by Sheriff")
	dependent, err := cmClient.Apply(t.Context(),
		corev1ac.ConfigMap("dependent", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion(sheriffsGVR.GroupVersion().String()).
				WithKind("Sheriff").
				WithName(owner.GetName()).
				WithUID(owner.GetUID())),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("Deleting the CRD itself")
	err = crdClient.Cluster(wsPath).Delete(t.Context(), sheriffCRD.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for Sheriff instance to be deleted")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := sheriffClient.Get(t.Context(), owner.GetName(), metav1.GetOptions{})
		return apierrors.IsNotFound(err), "sheriff instance still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "sheriff instance was not deleted")

	t.Logf("Waiting for ConfigMap dependent to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cmClient.Get(t.Context(), dependent.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), "configmap dependent still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "configmap dependent was not garbage collected")
}
