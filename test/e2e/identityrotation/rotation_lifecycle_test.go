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

package identityrotation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// deputiesGVR is a second, namespaced wildwest.dev resource that only this
// package serves. It must not reuse "sheriffs": the shared wildwest fixture
// serves sheriffs.wildwest.dev cluster-scoped, and a namespaced schema of the
// same group/resource on the same shard poisons the shard-wide watch cache
// for that resource ("couldn't compute key: Namespace parameter required"),
// breaking sheriff replication for every other package on the shared server.
var deputiesGVR = wildwestv1alpha1.SchemeGroupVersion.WithResource("deputies")

// installWildwestSchema installs a copy of the embedded cowboys
// APIResourceSchema under a different resource name, so one provider can
// serve two distinct resources (and a consumer can bind both).
func installWildwestSchema(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, schemaName, plural, singular, kind string) {
	t.Helper()

	raw, err := testFiles.ReadFile("apiresourceschema_cowboys.yaml")
	require.NoError(t, err)
	obj := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal(raw, &obj.Object))
	obj.SetName(schemaName)
	require.NoError(t, unstructured.SetNestedField(obj.Object, plural, "spec", "names", "plural"))
	require.NoError(t, unstructured.SetNestedField(obj.Object, singular, "spec", "names", "singular"))
	require.NoError(t, unstructured.SetNestedField(obj.Object, kind, "spec", "names", "kind"))
	require.NoError(t, unstructured.SetNestedField(obj.Object, kind+"List", "spec", "names", "listKind"))
	_, err = c.dynamic.Cluster(providerPath).Resource(apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas")).Create(ctx, obj, metav1.CreateOptions{})
	require.NoError(t, err)
}

// createWildwestExport creates an APIExport serving the given wildwest.dev
// resource from the given schema and returns its initial identity hash.
func createWildwestExport(ctx context.Context, t *testing.T, c rotationClients, providerPath logicalcluster.Path, exportName, resource, schemaName string) string {
	t.Helper()

	_, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name:    resource,
				Group:   "wildwest.dev",
				Schema:  schemaName,
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	var hash string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		hash = export.Status.IdentityHash
		return hash != "", "waiting for identity hash"
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	return hash
}

// bindExport binds the named export in the consumer workspace under the given
// binding name.
func bindExport(ctx context.Context, t *testing.T, c rotationClients, providerPath, consumerPath logicalcluster.Path, bindingName, exportName string) {
	t.Helper()

	t.Logf("Bind export %q in %q as %q", exportName, consumerPath, bindingName)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: bindingName},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: exportName},
				},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

// createWildwestObject creates a namespaced wildwest.dev object of the given
// resource/kind in the consumer workspace and returns its UID.
func createWildwestObject(ctx context.Context, t *testing.T, c rotationClients, consumerPath logicalcluster.Path, gvr schema.GroupVersionResource, kind, name string) types.UID {
	t.Helper()

	var uid types.UID
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := c.dynamic.Cluster(consumerPath).Resource(gvr).Namespace("default").Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": wildwestv1alpha1.SchemeGroupVersion.String(),
				"kind":       kind,
				"metadata":   map[string]interface{}{"name": name},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			obj, err = c.dynamic.Cluster(consumerPath).Resource(gvr).Namespace("default").Get(ctx, name, metav1.GetOptions{})
		}
		if err != nil {
			return false, err.Error()
		}
		uid = obj.GetUID()
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	return uid
}

// requireBindingOn asserts the named binding in the consumer workspace is
// fully on the given identity.
func requireBindingOn(ctx context.Context, t *testing.T, c rotationClients, consumerPath logicalcluster.Path, bindingName, hash string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		binding, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, bindingName, metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEmpty(collect, binding.Status.BoundResources)
		for _, br := range binding.Status.BoundResources {
			require.Equal(collect, hash, br.Schema.IdentityHash)
			require.Equal(collect, []string{hash}, br.IdentityHashes)
		}
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "binding %s in %s should be fully on identity %s", bindingName, consumerPath, hash)
}

// requireUnfenced asserts the consumer workspace's LogicalCluster does not
// carry the drain fence any more.
func requireUnfenced(ctx context.Context, t *testing.T, c rotationClients, consumerPath logicalcluster.Path) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		lc, err := c.kcp.Cluster(consumerPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		require.NoError(collect, err)
		owner, fenced := lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey]
		require.False(collect, fenced, "workspace %s is still fenced by %q", consumerPath, owner)
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "workspace %s should be unfenced after the drain", consumerPath)
}

// TestAPIExportIdentityRotationSharedFence rotates two exports that are both
// bound in the same consumer workspaces. The drain fence is one annotation on
// the workspace's LogicalCluster shared by both bindings' drains, which run
// concurrently on the shard's migrator workers: the fence must only be lifted
// once the last of them is done, and must not be left behind afterwards.
func TestAPIExportIdentityRotationSharedFence(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const consumerCount = 4

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())

	shards, err := c.kcp.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	shardNames := make([]string, 0, len(shards.Items))
	for _, shard := range shards.Items {
		shardNames = append(shardNames, shard.Name)
	}

	t.Logf("Create %d consumer workspaces across shards %v", consumerCount, shardNames)
	consumerPaths := make([]logicalcluster.Path, 0, consumerCount)
	for i := range consumerCount {
		opts := []kcptesting.UnprivilegedWorkspaceOption{kcptesting.WithName("consumer-%d", i+1)}
		if len(shardNames) > 1 {
			opts = append(opts, kcptesting.WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"name": shardNames[i%len(shardNames)]},
			}}))
		}
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, opts...)
		consumerPaths = append(consumerPaths, path)
	}

	t.Logf("Install two exports in %q: cowboys and deputies", providerPath)
	oldCowboysHash := installCowboysExport(ctx, t, c, providerPath)
	installWildwestSchema(ctx, t, c, providerPath, "today.deputies.wildwest.dev", "deputies", "deputy", "Deputy")
	oldDeputiesHash := createWildwestExport(ctx, t, c, providerPath, "today-deputies", "deputies", "today.deputies.wildwest.dev")

	cowboyUIDs := make([]types.UID, consumerCount)
	deputyUIDs := make([]types.UID, consumerCount)
	for i, consumerPath := range consumerPaths {
		bindExport(ctx, t, c, providerPath, consumerPath, "cowboys", "today-cowboys")
		bindExport(ctx, t, c, providerPath, consumerPath, "deputies", "today-deputies")
		cowboyUIDs[i] = createWildwestObject(ctx, t, c, consumerPath, cowboysGVR, "Cowboy", fmt.Sprintf("woody-%d", i+1))
		deputyUIDs[i] = createWildwestObject(ctx, t, c, consumerPath, deputiesGVR, "Deputy", fmt.Sprintf("earp-%d", i+1))
	}

	bindMigrationAPI(ctx, t, c, opsPath)
	createIdentitySecret(ctx, t, c, providerPath, "today-cowboys-rotated")
	createIdentitySecret(ctx, t, c, providerPath, "today-deputies-rotated")

	t.Logf("Rotate both exports back to back so both bindings of every consumer drain at the same time")
	requestRotation(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
		"rotate-today-cowboys", "today-cowboys-rotated", migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate})
	requestRotation(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-deputies"},
		"rotate-today-deputies", "today-deputies-rotated", migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate})

	t.Logf("Wait for both rotations to complete")
	newCowboysHash := waitForRotationPhaseWithin(ctx, t, c, opsPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationCompleted, 2*time.Minute)
	newDeputiesHash := waitForRotationPhaseWithin(ctx, t, c, opsPath, "rotate-today-deputies", migrationv1alpha1.APIExportIdentityRotationCompleted, 2*time.Minute)
	require.NotEqual(t, oldCowboysHash, newCowboysHash)
	require.NotEqual(t, oldDeputiesHash, newDeputiesHash)

	t.Logf("Every consumer has both bindings on the new identities, both objects survived, and the fence is gone")
	for i, consumerPath := range consumerPaths {
		requireBindingOn(ctx, t, c, consumerPath, "cowboys", newCowboysHash)
		requireBindingOn(ctx, t, c, consumerPath, "deputies", newDeputiesHash)
		requireUnfenced(ctx, t, c, consumerPath)

		cowboy, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, fmt.Sprintf("woody-%d", i+1), metav1.GetOptions{})
		require.NoError(t, err, "cowboy in %s must be readable after both drains", consumerPath)
		require.Equal(t, cowboyUIDs[i], cowboy.GetUID(), "cowboy UID must survive the drain")
		deputy, err := c.dynamic.Cluster(consumerPath).Resource(deputiesGVR).Namespace("default").Get(ctx, fmt.Sprintf("earp-%d", i+1), metav1.GetOptions{})
		require.NoError(t, err, "deputy in %s must be readable after both drains", consumerPath)
		require.Equal(t, deputyUIDs[i], deputy.GetUID(), "deputy UID must survive the drain")
	}
}

// TestAPIExportIdentityRotationLifecycle exercises the admission guards around
// an in-flight rotation: it cannot be deleted, no second rotation of the same
// export is admitted, and the After policy needs a duration. Once the alias
// is retired the rotation can be deleted.
func TestAPIExportIdentityRotationLifecycle(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	oldHash := installCowboysExport(ctx, t, c, providerPath)
	cowboyUID := bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, "woody")

	bindMigrationAPI(ctx, t, c, opsPath)
	createRotationSecret(ctx, t, c, providerPath)
	createIdentitySecret(ctx, t, c, providerPath, "today-cowboys-rotated-twice")

	exportRef := migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"}
	rotations := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportIdentityRotations()

	t.Logf("Request the rotation with Manual retirement to hold it in AliasActive")
	requestRotation(ctx, t, c, opsPath, exportRef, "rotate-today-cowboys", "today-cowboys-rotated",
		migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementManual})
	newHash := waitForRotationPhase(ctx, t, c, opsPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationAliasActive)
	require.NotEqual(t, oldHash, newHash)

	t.Logf("Deleting the in-flight rotation is rejected")
	err := rotations.Delete(ctx, "rotate-today-cowboys", metav1.DeleteOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err), "expected a forbidden error, got %v", err)
	require.ErrorContains(t, err, "AliasActive")
	_, err = rotations.Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
	require.NoError(t, err, "the rotation must still exist")

	t.Logf("A second rotation of the same export is rejected while the first is active")
	_, err = rotations.Create(ctx, &migrationv1alpha1.APIExportIdentityRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "rotate-again"},
		Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
			Export:          exportRef,
			NewIdentity:     apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: "today-cowboys-rotated-twice"}},
			AliasRetirement: migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err), "expected a forbidden error, got %v", err)
	require.ErrorContains(t, err, "still active")

	t.Logf("The After policy without a duration is rejected")
	_, err = rotations.Create(ctx, &migrationv1alpha1.APIExportIdentityRotation{
		ObjectMeta: metav1.ObjectMeta{Name: "rotate-after-without-duration"},
		Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
			Export:          exportRef,
			NewIdentity:     apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: "today-cowboys-rotated-twice"}},
			AliasRetirement: migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementAfter},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err), "expected a forbidden error, got %v", err)
	require.ErrorContains(t, err, "spec.aliasRetirement.after")

	t.Logf("Switching the active rotation to After without a duration is rejected too")
	rotation, err := rotations.Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	rotation.Spec.AliasRetirement = migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementAfter}
	_, err = rotations.Update(ctx, rotation, metav1.UpdateOptions{})
	require.Error(t, err)
	require.ErrorContains(t, err, "spec.aliasRetirement.after")

	t.Logf("Retire the alias by moving the policy to Immediate and wait for completion")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		rotation, err := rotations.Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		rotation.Spec.AliasRetirement = migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate}
		_, err = rotations.Update(ctx, rotation, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("moving retirement to Immediate: %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
	waitForRotationPhase(ctx, t, c, opsPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationCompleted)

	t.Logf("The export no longer carries the alias nor the active-rotation marker")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotContains(collect, export.Status.IdentityAliasHashes, oldHash)
		require.NotContains(collect, export.Annotations, migrationv1alpha1.ActiveRotationAnnotationKey)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)

	t.Logf("A completed rotation can be deleted")
	require.NoError(t, rotations.Delete(ctx, "rotate-today-cowboys", metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		_, err := rotations.Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
		require.True(collect, apierrors.IsNotFound(err), "expected the rotation to be gone, got %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)

	t.Logf("The consumer survived all of it")
	requireConsumerMigrated(ctx, t, c, consumerPath, newHash, "woody", cowboyUID)
}

// TestAPIExportIdentityRotationAfterPolicy rotates with the After policy: the
// alias must stay for the configured window after the drain completed and be
// retired on its own afterwards.
func TestAPIExportIdentityRotationAfterPolicy(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const aliasWindow = 15 * time.Second

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRotationClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	oldHash := installCowboysExport(ctx, t, c, providerPath)
	cowboyUID := bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, "woody")

	bindMigrationAPI(ctx, t, c, providerPath)
	createRotationSecret(ctx, t, c, providerPath)

	t.Logf("Request the rotation with an alias window of %s", aliasWindow)
	requestRotation(ctx, t, c, providerPath,
		migrationv1alpha1.ExportReference{Name: "today-cowboys"},
		"rotate-today-cowboys", "today-cowboys-rotated",
		migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementAfter, After: &metav1.Duration{Duration: aliasWindow}})

	newHash := waitForRotationPhase(ctx, t, c, providerPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationAliasActive)
	require.NotEqual(t, oldHash, newHash)

	rotation, err := c.kcp.Cluster(providerPath).MigrationV1alpha1().APIExportIdentityRotations().Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, rotation.Status.AliasActiveTimestamp, "AliasActive must record when the window opened")
	aliasActiveSince := rotation.Status.AliasActiveTimestamp.Time

	t.Logf("The alias is still published while the window is open")
	if time.Since(aliasActiveSince) < aliasWindow/2 {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		require.Contains(t, export.Status.IdentityAliasHashes, oldHash, "the alias must stay for the whole window")
	}

	t.Logf("The alias is retired on its own once the window has passed")
	waitForRotationPhaseWithin(ctx, t, c, providerPath, "rotate-today-cowboys", migrationv1alpha1.APIExportIdentityRotationCompleted, aliasWindow+wait.ForeverTestTimeout)

	rotation, err = c.kcp.Cluster(providerPath).MigrationV1alpha1().APIExportIdentityRotations().Get(ctx, "rotate-today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	retired := conditions.Get(rotation, migrationv1alpha1.IdentityRotationAliasRetired)
	require.NotNil(t, retired, "expected the AliasRetired condition")
	require.Equal(t, corev1.ConditionTrue, retired.Status)
	// condition timestamps have second granularity; allow for the truncation.
	require.False(t, retired.LastTransitionTime.Time.Before(aliasActiveSince.Add(aliasWindow-time.Second)),
		"the alias was retired at %s, before the window opened at %s ran out (%s)", retired.LastTransitionTime.Time, aliasActiveSince, aliasWindow)

	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, export.Status.IdentityAliasHashes, oldHash)

	requireConsumerMigrated(ctx, t, c, consumerPath, newHash, "woody", cowboyUID)
}
