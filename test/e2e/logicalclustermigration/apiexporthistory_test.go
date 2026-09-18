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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestFullMigrationAPIExportHistory verifies that the recorded resource scopes of an
// APIExport survive a logical cluster migration, including for a group resource whose
// APIResourceSchema was removed from the APIExport before the migration ran.
func TestFullMigrationAPIExportHistory(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, providerWs := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	providerLCName := logicalcluster.Name(providerWs.Spec.Cluster)

	t.Logf("Provider workspace %s (logical cluster %s) on shard %s", providerPath, providerLCName, originShard)

	schemas := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas()
	exports := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports()

	newSchema := func(name string, scope apiextensionsv1.ResourceScope) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "wildwest.dev",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "deputies",
					Singular: "deputy",
					Kind:     "Deputy",
					ListKind: "DeputyList",
				},
				Scope: scope,
				Versions: []apisv1alpha1.APIResourceVersion{{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema:  runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
				}},
			},
		}
	}

	t.Logf("Creating a namespaced and a cluster scoped APIResourceSchema for the same group resource")
	_, err = schemas.Create(t.Context(), newSchema("v1.deputies.wildwest.dev", apiextensionsv1.NamespaceScoped), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = schemas.Create(t.Context(), newSchema("v2.deputies.wildwest.dev", apiextensionsv1.ClusterScoped), metav1.CreateOptions{})
	require.NoError(t, err)

	resourceSchema := func(name string) apisv1alpha2.ResourceSchema {
		return apisv1alpha2.ResourceSchema{
			Name:    "deputies",
			Group:   "wildwest.dev",
			Schema:  name,
			Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
		}
	}

	setSchemas := func(resources ...apisv1alpha2.ResourceSchema) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			export, err := exports.Get(t.Context(), "deputies", metav1.GetOptions{})
			if err != nil {
				return err
			}
			export.Spec.Resources = resources
			_, err = exports.Update(t.Context(), export, metav1.UpdateOptions{})
			return err
		})
	}

	t.Logf("Creating the APIExport serving the namespaced schema")
	_, err = exports.Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "deputies"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{resourceSchema("v1.deputies.wildwest.dev")},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for the namespaced scope to be recorded on the origin shard")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := setSchemas(resourceSchema("v2.deputies.wildwest.dev"))
		if err == nil {
			return false, "cluster scoped schema was accepted"
		}
		if !apierrors.IsForbidden(err) {
			return false, err.Error()
		}
		return strings.Contains(err.Error(), "cannot be served with scope"), err.Error()
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Removing the schema from the APIExport so only the history carries the scope")
	require.NoError(t, setSchemas())

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "migration"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: core.RootCluster.Path().String(),
					Name: "migration.kcp.io",
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	t.Logf("Migrating provider logical cluster %s from %s to %s", providerLCName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
			ObjectMeta: metav1.ObjectMeta{Name: "provider-migration"},
			Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
				LogicalCluster:   providerLCName.String(),
				DestinationShard: destinationShard,
			},
		}, metav1.CreateOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	t.Logf("Waiting for the migration to complete")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration to complete")

	t.Logf("The removed group resource must still be rejected with the opposite scope on the destination shard")
	err = setSchemas(resourceSchema("v2.deputies.wildwest.dev"))
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err), "expected Forbidden, got %v", err)
	require.Contains(t, err.Error(), "cannot be served with scope")

	t.Logf("Re-adding the namespaced schema must still be allowed")
	require.NoError(t, setSchemas(resourceSchema("v1.deputies.wildwest.dev")))
}
