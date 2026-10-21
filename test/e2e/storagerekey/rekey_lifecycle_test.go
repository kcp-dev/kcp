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

package storagerekey

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

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
func installWildwestSchema(ctx context.Context, t *testing.T, c rekeyClients, providerPath logicalcluster.Path, schemaName, plural, singular, kind string) {
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
// resource from the given schema and returns its identity hash.
func createWildwestExport(ctx context.Context, t *testing.T, c rekeyClients, providerPath logicalcluster.Path, exportName, resource, schemaName string) string {
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
func bindExport(ctx context.Context, t *testing.T, c rekeyClients, providerPath, consumerPath logicalcluster.Path, bindingName, exportName string) {
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
func createWildwestObject(ctx context.Context, t *testing.T, c rekeyClients, consumerPath logicalcluster.Path, gvr schema.GroupVersionResource, kind, name string) types.UID {
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

// requireBindingOnKey asserts the named binding in the consumer workspace
// keeps the given identity and is fully on the given storage key.
func requireBindingOnKey(ctx context.Context, t *testing.T, c rekeyClients, consumerPath logicalcluster.Path, bindingName, identity, key string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		binding, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, bindingName, metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEmpty(collect, binding.Status.BoundResources)
		for _, br := range binding.Status.BoundResources {
			require.Equal(collect, identity, br.Schema.IdentityHash)
			require.Equal(collect, key, br.StorageKey())
			require.Empty(collect, br.Storage.PendingKeys)
		}
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "binding %s in %s should be fully on storage key %s", bindingName, consumerPath, key)
}

// requireUnfenced asserts the consumer workspace's LogicalCluster does not
// carry the drain fence any more.
func requireUnfenced(ctx context.Context, t *testing.T, c rekeyClients, consumerPath logicalcluster.Path) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		lc, err := c.kcp.Cluster(consumerPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		require.NoError(collect, err)
		owner, fenced := lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey]
		require.False(collect, fenced, "workspace %s is still fenced by %q", consumerPath, owner)
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "workspace %s should be unfenced after the drain", consumerPath)
}

// TestAPIExportStorageRekeySharedFence re-keys two exports that are both
// bound in the same consumer workspaces. The drain fence is one annotation on
// the workspace's LogicalCluster shared by both bindings' drains, which run
// concurrently on the shard's migrator workers: the fence must only be lifted
// once the last of them is done, and must not be left behind afterwards.
func TestAPIExportStorageRekeySharedFence(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const consumerCount = 4

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRekeyClients(t, server)

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
	cowboysIdentity := installCowboysExport(ctx, t, c, providerPath)
	installWildwestSchema(ctx, t, c, providerPath, "today.deputies.wildwest.dev", "deputies", "deputy", "Deputy")
	deputiesIdentity := createWildwestExport(ctx, t, c, providerPath, "today-deputies", "deputies", "today.deputies.wildwest.dev")

	cowboyUIDs := make([]types.UID, consumerCount)
	deputyUIDs := make([]types.UID, consumerCount)
	for i, consumerPath := range consumerPaths {
		bindExport(ctx, t, c, providerPath, consumerPath, "cowboys", "today-cowboys")
		bindExport(ctx, t, c, providerPath, consumerPath, "deputies", "today-deputies")
		cowboyUIDs[i] = createWildwestObject(ctx, t, c, consumerPath, cowboysGVR, "Cowboy", fmt.Sprintf("woody-%d", i+1))
		deputyUIDs[i] = createWildwestObject(ctx, t, c, consumerPath, deputiesGVR, "Deputy", fmt.Sprintf("earp-%d", i+1))
	}

	bindMigrationAPI(ctx, t, c, opsPath)

	t.Logf("Re-key both exports back to back so both bindings of every consumer drain at the same time")
	requestRekey(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
		"rekey-today-cowboys", "rekeyed-cowboys")
	requestRekey(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-deputies"},
		"rekey-today-deputies", "rekeyed-deputies")

	t.Logf("Wait for both re-keyings to complete")
	cowboysKey := waitForRekeyCompletedWithin(ctx, t, c, opsPath, "rekey-today-cowboys", 2*time.Minute)
	deputiesKey := waitForRekeyCompletedWithin(ctx, t, c, opsPath, "rekey-today-deputies", 2*time.Minute)
	require.Equal(t, "rekeyed-cowboys", cowboysKey)
	require.Equal(t, "rekeyed-deputies", deputiesKey)

	t.Logf("Every consumer has both bindings on the new storage keys, both objects survived, and the fence is gone")
	for i, consumerPath := range consumerPaths {
		requireBindingOnKey(ctx, t, c, consumerPath, "cowboys", cowboysIdentity, cowboysKey)
		requireBindingOnKey(ctx, t, c, consumerPath, "deputies", deputiesIdentity, deputiesKey)
		requireUnfenced(ctx, t, c, consumerPath)

		cowboy, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, fmt.Sprintf("woody-%d", i+1), metav1.GetOptions{})
		require.NoError(t, err, "cowboy in %s must be readable after both drains", consumerPath)
		require.Equal(t, cowboyUIDs[i], cowboy.GetUID(), "cowboy UID must survive the drain")
		deputy, err := c.dynamic.Cluster(consumerPath).Resource(deputiesGVR).Namespace("default").Get(ctx, fmt.Sprintf("earp-%d", i+1), metav1.GetOptions{})
		require.NoError(t, err, "deputy in %s must be readable after both drains", consumerPath)
		require.Equal(t, deputyUIDs[i], deputy.GetUID(), "deputy UID must survive the drain")
	}
}

// TestAPIExportStorageRekeyLifecycle exercises the admission guards around a
// re-keying: the spec is immutable, a re-keying for a resource the export
// does not serve fails validation, a completed re-keying can be deleted and
// deleting it lifts the cooldown, and the export carries no marker
// afterwards. Re-keying back onto the export's own identity restores the
// steady state.
func TestAPIExportStorageRekeyLifecycle(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRekeyClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	identity := installCowboysExport(ctx, t, c, providerPath)
	cowboyUID := bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, "woody")

	bindMigrationAPI(ctx, t, c, opsPath)

	exportRef := migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"}
	rekeys := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys()

	t.Logf("A re-keying of a resource the export does not serve fails validation")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := rekeys.Create(ctx, &migrationv1alpha1.APIExportStorageRekey{
			ObjectMeta: metav1.ObjectMeta{Name: "rekey-sheriffs"},
			Spec: migrationv1alpha1.APIExportStorageRekeySpec{
				Export:    exportRef,
				Resources: []apisv1alpha2.GroupResource{{Group: "wildwest.dev", Resource: "sheriffs"}},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating re-keying: %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rekey, err := rekeys.Get(ctx, "rekey-sheriffs", metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, migrationv1alpha1.APIExportStorageRekeyFailed, rekey.Status.Phase, "conditions: %v", rekey.Status.Conditions)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
	require.NoError(t, rekeys.Delete(ctx, "rekey-sheriffs", metav1.DeleteOptions{}), "a failed re-keying can be deleted")

	t.Logf("Re-key the export onto another prefix")
	requestRekey(ctx, t, c, opsPath, exportRef, "rekey-today-cowboys", "rekeyed-cowboys")
	target := waitForRekeyCompleted(ctx, t, c, opsPath, "rekey-today-cowboys")
	require.Equal(t, "rekeyed-cowboys", target)
	requireConsumerOnKey(ctx, t, c, consumerPath, identity, target, "woody", cowboyUID)

	t.Logf("The spec of a re-keying is immutable")
	rekey, err := rekeys.Get(ctx, "rekey-today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	rekey.Spec.TargetKey = "somewhere-else"
	_, err = rekeys.Update(ctx, rekey, metav1.UpdateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err) || apierrors.IsInvalid(err), "expected a forbidden or invalid error, got %v", err)

	t.Logf("A second re-keying of the same export is rejected within the cooldown")
	_, err = rekeys.Create(ctx, &migrationv1alpha1.APIExportStorageRekey{
		ObjectMeta: metav1.ObjectMeta{Name: "rekey-back"},
		Spec:       migrationv1alpha1.APIExportStorageRekeySpec{Export: exportRef},
	}, metav1.CreateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err), "expected a forbidden error, got %v", err)
	require.ErrorContains(t, err, "cooldown")

	t.Logf("The export no longer carries the active-rekey marker")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, identity, export.Status.IdentityHash)
		require.NotContains(collect, export.Annotations, migrationv1alpha1.ActiveRekeyAnnotationKey)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)

	t.Logf("A completed re-keying can be deleted, which lifts the cooldown")
	require.NoError(t, rekeys.Delete(ctx, "rekey-today-cowboys", metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		_, err := rekeys.Get(ctx, "rekey-today-cowboys", metav1.GetOptions{})
		require.True(collect, apierrors.IsNotFound(err), "expected the re-keying to be gone, got %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)

	t.Logf("Re-key back onto the export's own identity, the steady state")
	requestRekey(ctx, t, c, opsPath, exportRef, "rekey-back", "")
	target = waitForRekeyCompleted(ctx, t, c, opsPath, "rekey-back")
	require.Equal(t, identity, target, "an empty target key must resolve to the export's identity")

	t.Logf("The consumer survived all of it and is back on the identity prefix")
	requireConsumerOnKey(ctx, t, c, consumerPath, identity, identity, "woody", cowboyUID)
}
