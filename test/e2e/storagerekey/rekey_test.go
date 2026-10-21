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
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/apis/v1alpha2/permissionclaims"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/config/helpers"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

var cowboysGVR = wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys")

// rekeyClients bundles the cluster-scoped clients the re-keying tests use.
type rekeyClients struct {
	kcp     kcpclientset.ClusterInterface
	kube    kcpkubernetesclientset.ClusterInterface
	dynamic kcpdynamic.ClusterInterface
}

func newRekeyClients(t *testing.T, server kcptestingserver.RunningServer) rekeyClients {
	t.Helper()
	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)
	return rekeyClients{kcp: kcpClusterClient, kube: kubeClusterClient, dynamic: dynamicClusterClient}
}

// installCowboysExport installs the cowboys APIResourceSchema and APIExport in
// the provider workspace and returns the export's identity hash.
func installCowboysExport(ctx context.Context, t *testing.T, c rekeyClients, providerPath logicalcluster.Path) string {
	t.Helper()

	t.Logf("Install the cowboys APIResourceSchema and APIExport in %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(c.kcp.Cluster(providerPath).Discovery()))
	require.NoError(t, helpers.CreateResourceFromFS(ctx, c.dynamic.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))

	return createCowboysExport(ctx, t, c, providerPath, "today-cowboys", "today.cowboys.wildwest.dev")
}

// installCowboysSchema installs a copy of the embedded cowboys
// APIResourceSchema under the given name, so several exports can each own a
// schema (and thus a bound CRD) of their own.
func installCowboysSchema(ctx context.Context, t *testing.T, c rekeyClients, providerPath logicalcluster.Path, schemaName string) {
	t.Helper()

	raw, err := testFiles.ReadFile("apiresourceschema_cowboys.yaml")
	require.NoError(t, err)
	schema := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal(raw, &schema.Object))
	schema.SetName(schemaName)
	_, err = c.dynamic.Cluster(providerPath).Resource(apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas")).Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err)
}

// createCowboysExport creates an APIExport serving cowboys from the given
// schema and returns its identity hash.
func createCowboysExport(ctx context.Context, t *testing.T, c rekeyClients, providerPath logicalcluster.Path, exportName, schemaName string) string {
	t.Helper()

	_, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name:   "cowboys",
				Group:  "wildwest.dev",
				Schema: schemaName,
				Storage: apisv1alpha2.ResourceSchemaStorage{
					CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
				},
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

// bindCowboysAndCreate binds the cowboys export in the consumer workspace and
// creates a named cowboy, returning its UID.
func bindCowboysAndCreate(ctx context.Context, t *testing.T, c rekeyClients, providerPath, consumerPath logicalcluster.Path, cowboy string) types.UID {
	t.Helper()
	return bindExportAndCreateCowboy(ctx, t, c, providerPath, consumerPath, "today-cowboys", cowboy)
}

// bindExportAndCreateCowboy binds the named export in the consumer workspace
// (as APIBinding "cowboys") and creates a named cowboy, returning its UID.
func bindExportAndCreateCowboy(ctx context.Context, t *testing.T, c rekeyClients, providerPath, consumerPath logicalcluster.Path, exportName, cowboy string) types.UID {
	t.Helper()

	t.Logf("Bind export %q in %q and create cowboy %q", exportName, consumerPath, cowboy)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
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

	var uid types.UID
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": wildwestv1alpha1.SchemeGroupVersion.String(),
				"kind":       "Cowboy",
				"metadata":   map[string]interface{}{"name": cowboy},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			obj, err = c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, cowboy, metav1.GetOptions{})
		}
		if err != nil {
			return false, err.Error()
		}
		uid = obj.GetUID()
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	return uid
}

// bindMigrationAPI binds migration.kcp.io from root into the given workspace
// to obtain the re-keying capability there.
func bindMigrationAPI(ctx context.Context, t *testing.T, c rekeyClients, path logicalcluster.Path) {
	t.Helper()

	t.Logf("Bind the migration export in %q to obtain the re-keying capability", path)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(path).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "migration.kcp.io"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: core.RootCluster.Path().String(), Name: "migration.kcp.io"},
				},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("binding migration.kcp.io: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	kcptesting.WaitForAPIReady(t, c.kcp.Cluster(path).Discovery(), migrationv1alpha1.SchemeGroupVersion)
}

// requestRekey creates the re-keying object in rekeyPath, referencing the
// export via exportRef and draining onto targetKey (empty: the export's own
// identity). It tolerates the bound CRD establishment race after binding
// migration.kcp.io.
func requestRekey(ctx context.Context, t *testing.T, c rekeyClients, rekeyPath logicalcluster.Path, exportRef migrationv1alpha1.ExportReference, name, targetKey string) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(rekeyPath).MigrationV1alpha1().APIExportStorageRekeys().Create(ctx, &migrationv1alpha1.APIExportStorageRekey{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: migrationv1alpha1.APIExportStorageRekeySpec{
				Export:    exportRef,
				TargetKey: targetKey,
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating re-keying: %v", err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "creating the re-keying request")
}

// waitForRekeyCompleted waits until the re-keying completes and returns its
// resolved target key.
func waitForRekeyCompleted(ctx context.Context, t *testing.T, c rekeyClients, rekeyPath logicalcluster.Path, name string) string {
	t.Helper()
	return waitForRekeyCompletedWithin(ctx, t, c, rekeyPath, name, wait.ForeverTestTimeout)
}

// waitForRekeyCompletedWithin is waitForRekeyCompleted with an explicit
// timeout, for scenarios draining many bindings.
func waitForRekeyCompletedWithin(ctx context.Context, t *testing.T, c rekeyClients, rekeyPath logicalcluster.Path, name string, timeout time.Duration) string {
	t.Helper()
	var target string
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rekey, err := c.kcp.Cluster(rekeyPath).MigrationV1alpha1().APIExportStorageRekeys().Get(ctx, name, metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEqual(collect, migrationv1alpha1.APIExportStorageRekeyFailed, rekey.Status.Phase, "re-keying failed: %v", rekey.Status.Conditions)
		require.Equal(collect, migrationv1alpha1.APIExportStorageRekeyCompleted, rekey.Status.Phase,
			"re-keying phase %s, migrated %d/%d", rekey.Status.Phase, rekey.Status.MigratedBindings, rekey.Status.TotalBindings)
		// the drain is only over once every shard reported in and is
		// fully migrated; the top-level counters are the sums.
		require.NotEmpty(collect, rekey.Status.Shards, "expected per-shard drain reports")
		var total, migrated int32
		for _, shard := range rekey.Status.Shards {
			total += shard.TotalBindings
			migrated += shard.MigratedBindings
			require.Equal(collect, shard.TotalBindings, shard.MigratedBindings, "shard %s should be fully drained", shard.Shard)
		}
		require.Equal(collect, total, rekey.Status.TotalBindings, "totalBindings should be the sum of the shard entries")
		require.Equal(collect, migrated, rekey.Status.MigratedBindings, "migratedBindings should be the sum of the shard entries")
		// every re-keying test binds consumers before re-keying; a zero
		// total means the migrators failed to find the bindings.
		require.Positive(collect, total, "expected the shard entries to actually count the consumers' bindings")
		target = rekey.Status.TargetKey
	}, timeout, 250*time.Millisecond)
	return target
}

// requireConsumerOnKey asserts the consumer's binding keeps the export's
// identity, serves from the given storage key with nothing pending, and its
// cowboy survived the drain with the same UID.
func requireConsumerOnKey(ctx context.Context, t *testing.T, c rekeyClients, consumerPath logicalcluster.Path, identity, key, cowboy string, uid types.UID) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		binding, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEmpty(collect, binding.Status.BoundResources)
		for _, br := range binding.Status.BoundResources {
			require.Equal(collect, identity, br.Schema.IdentityHash, "the identity must never change")
			require.Equal(collect, key, br.StorageKey())
			require.Empty(collect, br.Storage.PendingKeys)
		}
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "binding in %s should be fully on storage key %s", consumerPath, key)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, cowboy, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, uid, obj.GetUID(), "UIDs must survive a storage drain")
	}, wait.ForeverTestTimeout, 250*time.Millisecond, "cowboy %s in %s should survive the drain", cowboy, consumerPath)
}

// TestAPIExportStorageRekey exercises the full storage re-keying flow of
// enhancements KEP 0008 with multiple consumers of the re-keyed export: bound
// resource instances of every consumer are drained storage-level onto a
// different prefix - preserving UIDs - while the export's identity, and
// therefore every reference to it, stays the same. Two consumers are pinned
// to the root shard so the shared bound CRD serving rebuild has to wait for
// both of their drains; a third is left unpinned so sharded test setups cover
// a consumer on another shard. The re-keying itself is requested from a
// separate ops workspace via spec.export.path - the delegation model: only
// the platform's ops workspace binds migration.kcp.io, the provider
// workspace never sees the capability.
func TestAPIExportStorageRekey(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRekeyClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithRootShard())
	// pin the third consumer to a non-root shard when one exists, so sharded
	// environments deterministically exercise a cross-shard drain and the
	// migrator's cross-shard progress report through the front-proxy.
	var nonRootShard string
	shards, err := c.kcp.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, shard := range shards.Items {
		if shard.Name != corev1alpha1.RootShard {
			nonRootShard = shard.Name
			break
		}
	}
	consumer3Opts := []kcptesting.UnprivilegedWorkspaceOption{kcptesting.WithName("consumer-3")}
	if nonRootShard != "" {
		t.Logf("Pinning consumer-3 to shard %q for a cross-shard drain", nonRootShard)
		consumer3Opts = append(consumer3Opts, kcptesting.WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"name": nonRootShard},
		}}))
	}

	consumerPaths := make([]logicalcluster.Path, 0, 3)
	for _, opts := range [][]kcptesting.UnprivilegedWorkspaceOption{
		{kcptesting.WithName("consumer-1"), kcptesting.WithRootShard()},
		{kcptesting.WithName("consumer-2"), kcptesting.WithRootShard()},
		consumer3Opts,
	} {
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, opts...)
		consumerPaths = append(consumerPaths, path)
	}

	identity := installCowboysExport(ctx, t, c, providerPath)

	cowboyUIDs := make([]types.UID, len(consumerPaths))
	for i, consumerPath := range consumerPaths {
		cowboyUIDs[i] = bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, fmt.Sprintf("woody-%d", i+1))
	}

	t.Logf("Every consumer starts on the export's identity as storage key")
	for i, consumerPath := range consumerPaths {
		requireConsumerOnKey(ctx, t, c, consumerPath, identity, identity, fmt.Sprintf("woody-%d", i+1), cowboyUIDs[i])
	}

	bindMigrationAPI(ctx, t, c, opsPath)

	const targetKey = "rekeyed-cowboys"
	t.Logf("Request the re-keying onto %q from the ops workspace %q, referencing the export by path", targetKey, opsPath)
	requestRekey(ctx, t, c, opsPath,
		migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
		"rekey-today-cowboys", targetKey)

	t.Logf("Wait for the re-keying to complete")
	target := waitForRekeyCompleted(ctx, t, c, opsPath, "rekey-today-cowboys")
	require.Equal(t, targetKey, target)

	t.Logf("The export's identity is untouched and the marker is gone")
	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, identity, export.Status.IdentityHash)
	require.NotContains(t, export.Annotations, migrationv1alpha1.ActiveRekeyAnnotationKey)

	t.Logf("Every consumer's binding is fully on the new storage key and every cowboy survived")
	for i, consumerPath := range consumerPaths {
		requireConsumerOnKey(ctx, t, c, consumerPath, identity, targetKey, fmt.Sprintf("woody-%d", i+1), cowboyUIDs[i])
	}

	if nonRootShard != "" {
		t.Logf("The non-root shard %q drained and reported its own binding", nonRootShard)
		rekey, err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys().Get(ctx, "rekey-today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		found := false
		for _, entry := range rekey.Status.Shards {
			if entry.Shard == nonRootShard {
				found = true
				require.Positive(t, entry.TotalBindings, "consumer-3 is pinned to shard %q; its binding must be counted there", nonRootShard)
				require.Equal(t, entry.TotalBindings, entry.MigratedBindings, "shard %q must be fully drained", nonRootShard)
			}
		}
		require.True(t, found, "expected a drain report from shard %q", nonRootShard)
	}

	t.Logf("A second re-keying within the cooldown is rejected by admission")
	_, err = c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys().Create(ctx, &migrationv1alpha1.APIExportStorageRekey{
		ObjectMeta: metav1.ObjectMeta{Name: "rekey-again"},
		Spec: migrationv1alpha1.APIExportStorageRekeySpec{
			Export: migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: "today-cowboys"},
		},
	}, metav1.CreateOptions{})
	require.Error(t, err, "expected the re-keying cooldown to reject an immediate second re-keying")
}

// TestAPIExportStorageRekeyKeepsClaims exercises cross-consumers of the
// re-keyed export: a second export claims the cowboys resource by the
// export's identity. Because a re-keying never changes the identity, the
// claim, its labels on claimed objects and the claimer's view keep working
// across the drain without any update.
func TestAPIExportStorageRekeyKeepsClaims(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := newRekeyClients(t, server)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"), kcptesting.WithRootShard())
	providerClusterName := logicalcluster.Name(providerWS.Spec.Cluster)

	identity := installCowboysExport(ctx, t, c, providerPath)
	cowboyUID := bindCowboysAndCreate(ctx, t, c, providerPath, consumerPath, "woody")

	// A second, unrelated cowboys export (own schema, hence own bound CRD) on
	// the same shard keeps the server's wildcard partial-metadata storage for
	// cowboys.wildwest.dev alive across the drain's bound-CRD rebuild. Without
	// it that storage is torn down and rebuilt together with the CRD, and the
	// wildcard informers behind the claim labeler get refreshed by accident
	// rather than by the drain itself.
	bystanderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("bystander"), kcptesting.WithRootShard())
	installCowboysSchema(ctx, t, c, bystanderPath, "bystander.cowboys.wildwest.dev")
	createCowboysExport(ctx, t, c, bystanderPath, "bystander-cowboys", "bystander.cowboys.wildwest.dev")
	bindExportAndCreateCowboy(ctx, t, c, bystanderPath, bystanderPath, "bystander-cowboys", "bystander")

	claim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: "cowboys"},
		Verbs:         []string{"*"},
		IdentityHash:  identity,
	}

	t.Logf("Install a claimer APIExport in %q claiming cowboys by identity %s", providerPath, identity)
	_, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-claimer"},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{claim},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind the claimer export in %q and accept the claim", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := c.kcp.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "claimer"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "today-claimer"},
				},
				PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: claim,
						Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				}},
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("creating claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	key, value, err := permissionclaims.ToLabelKeyAndValue(providerClusterName, "today-claimer", claim)
	require.NoError(t, err)
	requireCowboyClaimLabel := func(msg string) {
		t.Helper()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			obj, err := c.dynamic.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Get(ctx, "woody", metav1.GetOptions{})
			require.NoError(collect, err)
			require.Equal(collect, value, obj.GetLabels()[key], "cowboy labels: %v", obj.GetLabels())
		}, wait.ForeverTestTimeout, 250*time.Millisecond, msg)
	}

	t.Logf("Before the re-keying the cowboy carries the claim label derived from the identity")
	requireCowboyClaimLabel("claim label before the re-keying")

	bindMigrationAPI(ctx, t, c, providerPath)

	t.Logf("Re-key the export onto another prefix, requested from its own workspace")
	requestRekey(ctx, t, c, providerPath,
		migrationv1alpha1.ExportReference{Name: "today-cowboys"}, // empty path: same-workspace default
		"rekey-today-cowboys", "rekeyed-cowboys")
	target := waitForRekeyCompleted(ctx, t, c, providerPath, "rekey-today-cowboys")
	require.Equal(t, "rekeyed-cowboys", target)

	t.Logf("The identity is unchanged, so the claim label is unchanged, and the cowboy survived")
	export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, identity, export.Status.IdentityHash)
	requireConsumerOnKey(ctx, t, c, consumerPath, identity, target, "woody", cowboyUID)
	requireCowboyClaimLabel("claim label after the re-keying")
}

// TestAPIExportStorageRekeyConcurrent re-keys several exports at the same
// time, each bound by many consumers spread across all shards. It exercises
// the migrators fanning out over many drains at once, the per-shard progress
// reports of several re-keyings interleaving on the same shards, and the
// per-re-keying completeness gate not being confused by other re-keyings.
func TestAPIExportStorageRekeyConcurrent(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	const (
		exportCount   = 3
		consumerCount = 30
	)
	exportName := func(k int) string { return fmt.Sprintf("today-cowboys-%d", k+1) }
	schemaName := func(k int) string { return fmt.Sprintf("today-%d.cowboys.wildwest.dev", k+1) }
	targetKey := func(k int) string { return fmt.Sprintf("rekeyed-cowboys-%d", k+1) }
	rekeyName := func(k int) string { return fmt.Sprintf("rekey-today-cowboys-%d", k+1) }

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

	// consumer i binds export i%exportCount and lives on shard
	// (i/exportCount)%shards, so every export's consumers are spread over
	// every shard (a plain i%shards would correlate with the export choice
	// and put each export's consumers on a single shard).
	shardFor := func(i int) string { return shardNames[(i/exportCount)%len(shardNames)] }
	t.Logf("Create %d consumer workspaces across shards %v", consumerCount, shardNames)
	consumerPaths := make([]logicalcluster.Path, 0, consumerCount)
	for i := range consumerCount {
		opts := []kcptesting.UnprivilegedWorkspaceOption{kcptesting.WithName("consumer-%d", i+1)}
		if len(shardNames) > 1 {
			opts = append(opts, kcptesting.WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"name": shardFor(i)},
			}}))
		}
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, opts...)
		consumerPaths = append(consumerPaths, path)
	}

	// each export owns its own schema, and therefore its own bound CRD on
	// every shard, so the re-keyings' serving rebuilds are independent.
	identities := make([]string, exportCount)
	for k := range exportCount {
		installCowboysSchema(ctx, t, c, providerPath, schemaName(k))
		identities[k] = createCowboysExport(ctx, t, c, providerPath, exportName(k), schemaName(k))
	}

	cowboyUIDs := make([]types.UID, consumerCount)
	for i, consumerPath := range consumerPaths {
		cowboyUIDs[i] = bindExportAndCreateCowboy(ctx, t, c, providerPath, consumerPath, exportName(i%exportCount), fmt.Sprintf("woody-%d", i+1))
	}

	bindMigrationAPI(ctx, t, c, opsPath)

	t.Logf("Request %d re-keyings back to back", exportCount)
	for k := range exportCount {
		requestRekey(ctx, t, c, opsPath,
			migrationv1alpha1.ExportReference{Path: providerPath.String(), Name: exportName(k)},
			rekeyName(k), targetKey(k))
	}

	for k := range exportCount {
		t.Logf("Wait for re-keying %q to complete", rekeyName(k))
		target := waitForRekeyCompletedWithin(ctx, t, c, opsPath, rekeyName(k), 5*time.Minute)
		require.Equal(t, targetKey(k), target)

		rekey, err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys().Get(ctx, rekeyName(k), metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, int32(consumerCount/exportCount), rekey.Status.TotalBindings, "re-keying %q must have counted exactly its own export's bindings", rekeyName(k))
		reported := sets.New[string]()
		for _, entry := range rekey.Status.Shards {
			reported.Insert(entry.Shard)
			require.Equal(t, entry.TotalBindings, entry.MigratedBindings, "shard %q must be fully drained for %q", entry.Shard, rekeyName(k))
		}
		require.True(t, reported.HasAll(shardNames...), "every shard must have reported for %q, got %v", rekeyName(k), sets.List(reported))

		export, err := c.kcp.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName(k), metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, identities[k], export.Status.IdentityHash, "re-keying must not touch the identity of %q", exportName(k))
	}

	t.Logf("Every consumer's binding is fully on its export's new storage key and every cowboy survived")
	for i, consumerPath := range consumerPaths {
		requireConsumerOnKey(ctx, t, c, consumerPath, identities[i%exportCount], targetKey(i%exportCount), fmt.Sprintf("woody-%d", i+1), cowboyUIDs[i])
	}
}
