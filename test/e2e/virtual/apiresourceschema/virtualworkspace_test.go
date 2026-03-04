/*
Copyright 2026 The KCP Authors.

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

package apiresourceschema

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/pkg/virtual/apiresourceschema"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/authfixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIResourceSchemaVirtualWorkspaceDiscovery tests that the apiresourceschema virtual workspace
// exposes the correct API discovery information.
func TestAPIResourceSchemaVirtualWorkspaceDiscovery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	// Create organization workspace
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create a provider workspace with an APIExport
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// Create a consumer workspace
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	// Create APIResourceSchema and APIExport in provider
	group := framework.UniqueGroup(".discovery.test")
	t.Logf("Creating sheriffs schema and export in provider with group %s", group)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, providerPath, kcpClusterClient, group, "discovery test sheriffs")

	// Wait for APIExport to be ready
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	// Bind consumer to provider
	t.Logf("Binding consumer %s to provider export %s", consumerPath, group)
	apifixtures.BindToExport(t.Context(), t, providerPath, group, consumerPath, kcpClusterClient)

	// Wait for binding to be bound
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	// Set up the VW URL
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	vwURL := fmt.Sprintf("%s/services/%s/%s", rootShardCfg.Host, apiresourceschema.VirtualWorkspaceName, consumerClusterName)
	vwCfg := rest.CopyConfig(rootShardCfg)
	vwCfg.Host = vwURL

	virtualWorkspaceDiscoveryClient, err := kcpdiscovery.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Logf("Checking discovery for apiresourceschema VW at %s", vwURL)
	_, apiResourceLists, err := virtualWorkspaceDiscoveryClient.ServerGroupsAndResources()
	require.NoError(t, err)

	// Find the apis.kcp.io/v1alpha1 group
	var found bool
	for _, list := range apiResourceLists {
		if list.GroupVersion == apisv1alpha1.SchemeGroupVersion.String() {
			for _, r := range list.APIResources {
				if r.Name == "apiresourceschemas" {
					found = true
					// Verify it only has read-only verbs
					verbSet := sets.New(r.Verbs...)
					require.True(t, verbSet.Has("get"), "expected get verb")
					require.True(t, verbSet.Has("list"), "expected list verb")
					require.True(t, verbSet.Has("watch"), "expected watch verb")
					require.False(t, verbSet.Has("create"), "should not have create verb")
					require.False(t, verbSet.Has("update"), "should not have update verb")
					require.False(t, verbSet.Has("delete"), "should not have delete verb")
					break
				}
			}
			break
		}
	}
	require.True(t, found, "expected to find apiresourceschemas resource in discovery, got: %v", apiResourceLists)
}

// TestAPIResourceSchemaVirtualWorkspaceAccess tests that consumers can access APIResourceSchemas
// for all their APIBindings through the apiresourceschema virtual workspace.
func TestAPIResourceSchemaVirtualWorkspaceAccess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	// Create organization workspace
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create two provider workspaces with different APIExports
	provider1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	provider2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// Create a consumer workspace
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	// Create APIResourceSchemas and APIExports in provider1
	group1 := framework.UniqueGroup(".provider1.test")
	t.Logf("Creating sheriffs schema and export in provider1 with group %s", group1)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, provider1Path, kcpClusterClient, group1, "provider1 sheriffs")

	// Wait for APIExport to be ready
	t.Logf("Waiting for APIExport %s in %s to have identity", group1, provider1Path)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(provider1Path).ApisV1alpha2().APIExports().Get(t.Context(), group1, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	// Create APIResourceSchemas and APIExports in provider2
	group2 := framework.UniqueGroup(".provider2.test")
	t.Logf("Creating sheriffs schema and export in provider2 with group %s", group2)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, provider2Path, kcpClusterClient, group2, "provider2 sheriffs")

	// Wait for APIExport to be ready
	t.Logf("Waiting for APIExport %s in %s to have identity", group2, provider2Path)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(provider2Path).ApisV1alpha2().APIExports().Get(t.Context(), group2, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	// Bind consumer to both providers
	t.Logf("Binding consumer %s to provider1 export %s", consumerPath, group1)
	apifixtures.BindToExport(t.Context(), t, provider1Path, group1, consumerPath, kcpClusterClient)

	t.Logf("Binding consumer %s to provider2 export %s", consumerPath, group2)
	apifixtures.BindToExport(t.Context(), t, provider2Path, group2, consumerPath, kcpClusterClient)

	// Wait for bindings to be bound
	t.Logf("Waiting for APIBinding %s to be bound", group1)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), group1, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Waiting for APIBinding %s to be bound", group2)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), group2, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	// Set up the VW client
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	vwURL := fmt.Sprintf("%s/services/%s/%s", rootShardCfg.Host, apiresourceschema.VirtualWorkspaceName, consumerClusterName)
	vwCfg := rest.CopyConfig(rootShardCfg)
	vwCfg.Host = vwURL

	vwKcpClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err, "failed to construct kcp client for VW")

	// Get the expected schema names from the APIExports
	export1, err := kcpClusterClient.Cluster(provider1Path).ApisV1alpha2().APIExports().Get(t.Context(), group1, metav1.GetOptions{})
	require.NoError(t, err)
	export2, err := kcpClusterClient.Cluster(provider2Path).ApisV1alpha2().APIExports().Get(t.Context(), group2, metav1.GetOptions{})
	require.NoError(t, err)

	expectedSchemas := sets.New[string]()
	for _, resource := range export1.Spec.Resources {
		expectedSchemas.Insert(resource.Schema)
	}
	for _, resource := range export2.Spec.Resources {
		expectedSchemas.Insert(resource.Schema)
	}

	t.Logf("Expected schemas from bindings: %v", sets.List(expectedSchemas))

	// Test listing schemas through the VW
	t.Logf("Listing APIResourceSchemas through VW at %s", vwURL)
	var actualSchemas sets.Set[string]
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		// Note: The VW URL already contains the consumer cluster, so we use consumerClusterName here.
		// The /clusters/<path>/ part of the URL is stripped by the VW.
		schemas, err := vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing schemas: %v", err)
		}

		actualSchemas = sets.New[string]()
		for _, schema := range schemas.Items {
			actualSchemas.Insert(schema.Name)
		}

		// Check that all expected schemas are present in the result.
		// There may be additional schemas from built-in bindings (e.g., tenancy.kcp.io).
		if !actualSchemas.IsSuperset(expectedSchemas) {
			return false, fmt.Sprintf("expected schemas %v to be subset of actual %v", sets.List(expectedSchemas), sets.List(actualSchemas))
		}

		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to see all bound schemas through VW")

	t.Logf("Successfully listed %d schemas through VW: %v", actualSchemas.Len(), sets.List(actualSchemas))

	// Test getting individual schemas
	for schemaName := range expectedSchemas {
		t.Logf("Getting schema %s through VW", schemaName)
		schema, err := vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(t.Context(), schemaName, metav1.GetOptions{})
		require.NoError(t, err, "failed to get schema %s", schemaName)
		require.Equal(t, schemaName, schema.Name)
	}

	// Test that non-bound schemas are not accessible
	t.Logf("Verifying that non-bound schemas are not accessible")
	_, err = vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(t.Context(), "non-existent-schema", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "expected NotFound error for non-existent schema, got: %v", err)
}

// TestAPIResourceSchemaVirtualWorkspaceReadOnly tests that the apiresourceschema virtual workspace
// only allows read-only operations.
func TestAPIResourceSchemaVirtualWorkspaceReadOnly(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	// Create organization workspace
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create a provider workspace
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// Create a consumer workspace
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	// Create APIResourceSchema and APIExport in provider
	group := framework.UniqueGroup(".readonly.test")
	t.Logf("Creating sheriffs schema and export in provider with group %s", group)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, providerPath, kcpClusterClient, group, "readonly test sheriffs")

	// Wait for APIExport to be ready
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	// Bind consumer to provider
	t.Logf("Binding consumer %s to provider export %s", consumerPath, group)
	apifixtures.BindToExport(t.Context(), t, providerPath, group, consumerPath, kcpClusterClient)

	// Wait for binding to be bound
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	// Set up the VW client
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	vwURL := fmt.Sprintf("%s/services/%s/%s", rootShardCfg.Host, apiresourceschema.VirtualWorkspaceName, consumerClusterName)
	vwCfg := rest.CopyConfig(rootShardCfg)
	vwCfg.Host = vwURL

	vwKcpClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err, "failed to construct kcp client for VW")

	// Get the schema name from the APIExport
	export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), group, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, export.Spec.Resources, 1)
	schemaName := export.Spec.Resources[0].Schema

	// Wait for schema to be available
	// Note: The VW URL already contains the consumer cluster, so we use consumerClusterName here.
	// The /clusters/<path>/ part of the URL is stripped by the VW, but we still need to provide
	// a concrete cluster to the client (not wildcard).
	var schema *apisv1alpha1.APIResourceSchema
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		schema, err = vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(t.Context(), schemaName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting schema: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to get schema through VW")

	t.Logf("Testing that create is denied")
	newSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.new.schema",
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "test.kcp.io",
			Names: schema.Spec.Names,
			Scope: schema.Spec.Scope,
		},
	}
	_, err = vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), newSchema, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err) || apierrors.IsMethodNotSupported(err), "expected Forbidden or MethodNotSupported for create, got: %v", err)

	t.Logf("Testing that update is denied")
	schemaCopy := schema.DeepCopy()
	if schemaCopy.Annotations == nil {
		schemaCopy.Annotations = map[string]string{}
	}
	schemaCopy.Annotations["test"] = "value"
	_, err = vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Update(t.Context(), schemaCopy, metav1.UpdateOptions{})
	require.True(t, apierrors.IsForbidden(err) || apierrors.IsMethodNotSupported(err), "expected Forbidden or MethodNotSupported for update, got: %v", err)

	t.Logf("Testing that delete is denied")
	err = vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Delete(t.Context(), schemaName, metav1.DeleteOptions{})
	require.True(t, apierrors.IsForbidden(err) || apierrors.IsMethodNotSupported(err), "expected Forbidden or MethodNotSupported for delete, got: %v", err)

	t.Logf("Verified that VW is read-only")
}

// TestAPIResourceSchemaVirtualWorkspaceMultipleBindings tests that when a consumer has multiple
// APIBindings to different APIExports, all referenced schemas are accessible.
func TestAPIResourceSchemaVirtualWorkspaceMultipleBindings(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	// Create organization workspace
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create multiple provider workspaces
	numProviders := 3
	providers := make([]logicalcluster.Path, numProviders)
	groups := make([]string, numProviders)
	for i := range numProviders {
		providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
		providers[i] = providerPath
		groups[i] = framework.UniqueGroup(fmt.Sprintf(".provider%d.test", i+1))

		t.Logf("Creating sheriffs schema and export in %s with group %s", providerPath, groups[i])
		apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, providerPath, kcpClusterClient, groups[i], fmt.Sprintf("provider%d sheriffs", i+1))

		// Wait for APIExport to be ready
		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), groups[i], metav1.GetOptions{})
		}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	}

	// Create consumer workspace
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	// Bind consumer to all providers
	expectedSchemas := sets.New[string]()
	for i := range numProviders {
		t.Logf("Binding consumer to %s export %s", providers[i], groups[i])
		apifixtures.BindToExport(t.Context(), t, providers[i], groups[i], consumerPath, kcpClusterClient)

		// Get the schema name
		export, err := kcpClusterClient.Cluster(providers[i]).ApisV1alpha2().APIExports().Get(t.Context(), groups[i], metav1.GetOptions{})
		require.NoError(t, err)
		for _, resource := range export.Spec.Resources {
			expectedSchemas.Insert(resource.Schema)
		}
	}

	// Wait for all bindings to be bound
	for i := range numProviders {
		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), groups[i], metav1.GetOptions{})
		}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))
	}

	// Set up the VW client
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	vwURL := fmt.Sprintf("%s/services/%s/%s", rootShardCfg.Host, apiresourceschema.VirtualWorkspaceName, consumerClusterName)
	vwCfg := rest.CopyConfig(rootShardCfg)
	vwCfg.Host = vwURL

	vwKcpClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err, "failed to construct kcp client for VW")

	t.Logf("Expected schemas from %d bindings: %v", numProviders, sets.List(expectedSchemas))

	// Test listing schemas through the VW
	// Note: The VW URL already contains the consumer cluster, so we use consumerClusterName here.
	// The /clusters/<path>/ part of the URL is stripped by the VW.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		schemas, err := vwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing schemas: %v", err)
		}

		actualSchemas := sets.New[string]()
		for _, schema := range schemas.Items {
			actualSchemas.Insert(schema.Name)
		}

		// Check that all expected schemas are present in the result.
		// There may be additional schemas from built-in bindings (e.g., tenancy.kcp.io).
		if !actualSchemas.IsSuperset(expectedSchemas) {
			return false, fmt.Sprintf("expected schemas %v to be subset of actual %v", sets.List(expectedSchemas), sets.List(actualSchemas))
		}

		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to see all %d schemas from %d bindings through VW", expectedSchemas.Len(), numProviders)

	t.Logf("Successfully verified access to %d schemas from %d different APIBindings", expectedSchemas.Len(), numProviders)
}

// TestAPIResourceSchemaVirtualWorkspaceAuthorization tests that access to the apiresourceschema
// virtual workspace is properly gated by RBAC permissions on APIBindings in the consumer workspace.
//
// This test verifies:
//  1. A ServiceAccount in the provider workspace without permission cannot access the VW
//  2. After granting permission using the global kcp SA format (system:kcp:serviceaccount:<cluster>:<namespace>:<name>),
//     the provider's ServiceAccount can access schemas through the consumer's VW
//
// This demonstrates cross-workspace ServiceAccount access using the kcp global SA identity format,
// where ClusterNameKey is extracted from the SA token's JWT claims.
func TestAPIResourceSchemaVirtualWorkspaceAuthorization(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client")

	// Create organization workspace
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create a provider workspace
	providerPath, providerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"))
	providerClusterName := logicalcluster.Name(providerWorkspace.Spec.Cluster)

	// Create a consumer workspace
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	// Create APIResourceSchema and APIExport in provider
	group := framework.UniqueGroup(".authtest")
	t.Logf("Creating sheriffs schema and export in provider with group %s", group)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, providerPath, kcpClusterClient, group, "auth test sheriffs")

	// Wait for APIExport to be ready
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	// Bind consumer to provider
	t.Logf("Binding consumer %s to provider export %s", consumerPath, group)
	apifixtures.BindToExport(t.Context(), t, providerPath, group, consumerPath, kcpClusterClient)

	// Wait for binding to be bound
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), group, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	// Get the schema name from the APIExport
	export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), group, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, export.Spec.Resources, 1)
	schemaName := export.Spec.Resources[0].Schema

	// Create a ServiceAccount in the PROVIDER workspace
	// This simulates a provider operator SA that needs to access consumer schema info
	t.Log("Creating ServiceAccount in provider workspace")
	sa, tokenSecret := authfixtures.CreateServiceAccount(t, kubeClusterClient, providerPath, "default", "provider-operator-")
	t.Logf("Created ServiceAccount %s in provider workspace %s", sa.Name, providerPath)

	// Set up the VW URL and client using the ServiceAccount token
	// The SA from provider workspace accesses the VW scoped to the consumer cluster
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	vwURL := fmt.Sprintf("%s/services/%s/%s", rootShardCfg.Host, apiresourceschema.VirtualWorkspaceName, consumerClusterName)

	// Create a config using the ServiceAccount token
	saConfig := framework.ConfigWithToken(string(tokenSecret.Data["token"]), cfg)
	saConfig.Host = vwURL

	saVwKcpClient, err := kcpclientset.NewForConfig(saConfig)
	require.NoError(t, err, "failed to construct kcp client for VW with ServiceAccount")

	// Test 1: ServiceAccount WITHOUT permission should be denied
	t.Log("Testing that provider ServiceAccount WITHOUT permission to read APIBindings is denied access")
	_, err = saVwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().List(t.Context(), metav1.ListOptions{})
	require.Error(t, err, "expected error when accessing VW without permission")
	require.True(t, apierrors.IsForbidden(err), "expected Forbidden error, got: %v", err)

	// Test 2: Grant the provider's ServiceAccount permission to read APIBindings in the consumer workspace
	// using the global kcp SA format: system:kcp:serviceaccount:<cluster>:<namespace>:<name>
	// This demonstrates cross-workspace SA identity matching via ClusterNameKey
	globalSAName := fmt.Sprintf("system:kcp:serviceaccount:%s:default:%s", providerClusterName, sa.Name)
	t.Logf("Granting global SA %s permission to read APIBindings in consumer workspace", globalSAName)

	// Create a ClusterRole that allows:
	// 1. "access" verb on "/" - required by workspaceContentAuthorizer for workspace access
	// 2. reading APIBindings - required by apiResourceSchemaAuthorizer for schema access
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "apibinding-reader",
		},
		Rules: []rbacv1.PolicyRule{
			{
				// Workspace access permission - required for foreign SAs to access the workspace
				NonResourceURLs: []string{"/"},
				Verbs:           []string{"access"},
			},
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ClusterRole")

	// Create a ClusterRoleBinding using the global kcp SA identity format
	// This binds to "system:kcp:serviceaccount:<provider-cluster>:default:<sa-name>"
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-sa-apibinding-reader",
		},
		Subjects: []rbacv1.Subject{
			{
				APIGroup: "",
				Kind:     "User",
				Name:     globalSAName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     clusterRole.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ClusterRoleBinding")

	// Test 3: Provider's ServiceAccount WITH permission should be able to access schemas in consumer's VW
	t.Log("Testing that provider ServiceAccount WITH permission can access consumer's schemas through VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		schemas, err := saVwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing schemas: %v", err)
		}
		// Check that we can see at least the expected schema
		for _, schema := range schemas.Items {
			if schema.Name == schemaName {
				return true, ""
			}
		}
		return false, fmt.Sprintf("expected schema %s not found in list", schemaName)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected provider ServiceAccount to access consumer schemas after RBAC grant")

	// Test 4: ServiceAccount can get individual schema
	t.Logf("Testing that provider ServiceAccount can get schema %s", schemaName)
	schema, err := saVwKcpClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(t.Context(), schemaName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get schema with provider ServiceAccount")
	require.Equal(t, schemaName, schema.Name)

	t.Log("Successfully verified cross-workspace ServiceAccount authorization using global kcp SA identity")
}
