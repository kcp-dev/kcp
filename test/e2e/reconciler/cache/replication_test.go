/*
Copyright 2022 The KCP Authors.

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

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface)
}

// scenarios all test scenarios that will be run against an environment provided by the test binary.
var scenarios = []testScenario{
	{"TestReplicateAPIExport", replicateAPIExportScenario},
	{"TestReplicateAPIExportNegative", replicateAPIExportNegativeScenario},
	{"TestReplicateAPIResourceSchema", replicateAPIResourceSchemaScenario},
	{"TestReplicateAPIResourceSchemaNegative", replicateAPIResourceSchemaNegativeScenario},
	{"TestReplicateWorkspaceType", replicateWorkspaceTypeScenario},
	{"TestReplicateWorkspaceTypeNegative", replicateWorkspaceTypeNegativeScenario},
}

// disruptiveScenarios contains a list of scenarios that will be run in a private environment
// so that they don't disrupt other tests.
var disruptiveScenarios = []testScenario{
	{"TestReplicateShard", replicateShardScenario},
	{"TestReplicateShardNegative", replicateShardNegativeScenario},
}

// replicateAPIResourceSchemaScenario tests if an APIResourceSchema is propagated to the cache server.
// The test exercises creation, modification and removal of the APIResourceSchema object.
func replicateAPIResourceSchemaScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResource(
		ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"APIResourceSchema",
		apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
		&apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s.%s", withPseudoRandomSuffix("today"), "sheriffs.wild.wild.west"),
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "wild.wild.west",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "sheriffs",
					Singular: "sheriff",
					Kind:     "Sheriff",
					ListKind: "SheriffList",
				},
				Scope: "Namespaced",
				Versions: []apisv1alpha1.APIResourceVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: runtime.RawExtension{
							Raw: func() []byte {
								ret, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
									Type:        "object",
									Description: "the best sheriff out there",
								})
								if err != nil {
									panic(err)
								}
								return ret
							}(),
						},
					},
				},
			},
		},
		nil)
}

// replicateAPIResourceSchemaNegativeScenario checks if modified or even deleted cached APIResourceSchema will be reconciled to match the original object.
func replicateAPIResourceSchemaNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResourceNegative(
		ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"APIResourceSchema",
		apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
		&apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s.%s", withPseudoRandomSuffix("juicy"), "mangodbs.db.io"),
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "db.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "mangodbs",
					Singular: "mangodb",
					Kind:     "MangoDB",
					ListKind: "MangoDBList",
				},
				Scope: "Namespaced",
				Versions: []apisv1alpha1.APIResourceVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: runtime.RawExtension{
							Raw: func() []byte {
								ret, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
									Type:        "object",
									Description: "the best db out there",
								})
								if err != nil {
									panic(err)
								}

								return ret
							}(),
						},
					},
				},
			},
		},
		nil,
	)
}

// replicateAPIExportScenario tests if an APIExport is propagated to the cache server.
// The test exercises creation, modification and removal of the APIExport object.
func replicateAPIExportScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResource(ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"APIExport",
		apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
		&apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: withPseudoRandomSuffix("wild.wild.west")},
		},
		&apisv1alpha1.APIExport{
			Spec: apisv1alpha1.APIExportSpec{LatestResourceSchemas: []string{"foo.bar"}},
		},
	)
}

// replicateAPIExportNegativeScenario checks if modified or even deleted cached APIExport will be reconciled to match the original object.
func replicateAPIExportNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResourceNegative(
		ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"APIExport",
		apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
		&apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: withPseudoRandomSuffix("mangodb"),
			},
		},
		&apisv1alpha1.APIExport{
			Spec: apisv1alpha1.APIExportSpec{LatestResourceSchemas: []string{"foo"}},
		},
	)
}

// replicateShardScenario tests if a Shard is propagated to the cache server.
// The test exercises creation, modification and removal of the Shard object.
func replicateShardScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResource(ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		core.RootCluster,
		"Shard",
		corev1alpha1.SchemeGroupVersion.WithResource("shards"),
		&corev1alpha1.Shard{
			ObjectMeta: metav1.ObjectMeta{Name: withPseudoRandomSuffix("test-shard")},
			Spec:       corev1alpha1.ShardSpec{BaseURL: "https://base.kcp.test.dev"},
		},
		&corev1alpha1.Shard{
			Spec: corev1alpha1.ShardSpec{BaseURL: "https://kcp.test.dev"},
		},
	)
}

// replicateShardNegativeScenario checks if modified or even deleted cached Shard will be reconciled to match the original object.
func replicateShardNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResourceNegative(
		ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		core.RootCluster,
		"Shard",
		corev1alpha1.SchemeGroupVersion.WithResource("shards"),
		&corev1alpha1.Shard{
			ObjectMeta: metav1.ObjectMeta{
				Name: withPseudoRandomSuffix("test-shard"),
			},
			Spec: corev1alpha1.ShardSpec{
				BaseURL: "https://base.kcp.test.dev",
			},
		},
		&corev1alpha1.Shard{
			Spec: corev1alpha1.ShardSpec{BaseURL: "https://base2.kcp.test.dev"},
		},
	)
}

// replicateWorkspaceTypeScenario tests if a WorkspaceType is propagated to the cache server.
// The test exercises creation, modification and removal of the Shard object.
func replicateWorkspaceTypeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResource(ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"WorkspaceType",
		tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"),
		&tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: withPseudoRandomSuffix("replicate-workspace-type")},
			Spec:       tenancyv1alpha1.WorkspaceTypeSpec{},
		},
		&tenancyv1alpha1.WorkspaceType{
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{AdditionalWorkspaceLabels: map[string]string{"foo": "bar"}},
		},
	)
}

// replicateWorkspaceTypeNegativeScenario checks if modified or even deleted cached WorkspaceType will be reconciled to match the original object.
func replicateWorkspaceTypeNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()
	replicateResourceNegative(
		ctx,
		t,
		server,
		kcpShardClusterDynamicClient,
		cacheKcpClusterDynamicClient,
		"",
		"WorkspaceType",
		tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"),
		&tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: withPseudoRandomSuffix("replicate-workspace-type-negative")},
			Spec:       tenancyv1alpha1.WorkspaceTypeSpec{},
		},
		&tenancyv1alpha1.WorkspaceType{
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{AdditionalWorkspaceLabels: map[string]string{"foo": "bar"}},
		},
	)
}

// replicateResource tests if the given resource is propagated to the cache server.
// The test exercises creation, modification and removal of the resource.
//
// note that adding a new scenario requires providing a resource along with its type information
// to this function, i.e.:
//
//	replicateResource(
//	  ...
//	  "root:org:rh", // clusterName
//	  "APIExports", // kind
//	  apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"), // gvr
//	  &apisv1alpha1.APIExport{...}, // the resource
//	  &apisv1alpha1.APIExport{Spec: apisv1alpha1.APIExportSpec{...}}, // the resource with its spec modified
//	)
func replicateResource(ctx context.Context, t *testing.T,
	server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Name, /*cluster for hosting the provided resource, can be empty*/
	kind string, /*kind for the given resource*/
	gvr schema.GroupVersionResource, /*gvr for the given resource*/
	res runtime.Object, /*a strongly typed resource object that will be created*/
	resWithModifiedSpec runtime.Object /*a strongly typed resource obj with modified spec only, will be used for an update*/) {
	t.Helper()

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	if clusterName.Empty() {
		_, ws := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithRootShard())
		clusterName = logicalcluster.Name(ws.Spec.Cluster)
	}
	resMeta, err := meta.Accessor(res)
	require.NoError(t, err)
	resourceName := resMeta.GetName()
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: kind, gvr: gvr, cluster: clusterName, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source %s %s/%s on the root shard for replication", kind, clusterName, resourceName)
	scenario.CreateSourceResource(ctx, t, res)
	t.Logf("Verify that the source %s %s/%s was replicated to the cache server", kind, clusterName, resourceName)
	scenario.VerifyReplication(ctx, t)

	if resWithModifiedSpec != nil {
		t.Logf("Change the spec on source %s %s/%s and verify if updates were propagated to the cached object", kind, clusterName, resourceName)
		scenario.UpdateSpecSourceResource(ctx, t, resWithModifiedSpec)
		scenario.VerifyReplication(ctx, t)
	}

	// note that for some res the spec of an is immutable we are limited to changing some metadata
	t.Logf("Change some metadata on source %s %s/%s and verify if updates were propagated to the cached object", kind, clusterName, resourceName)
	scenario.UpdateMetaSourceResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Verify that deleting source %s %s/%s leads to removal of the cached object", kind, clusterName, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t)
}

// replicateResourceNegative checks if modified or even deleted cached resource will be reconciled to match the original object.
//
// note that adding a new scenario requires providing a resource along with its type information
// to this function, i.e.:
//
//	replicateResourceNegative(
//	  ...
//	  "root:org:rh", // clusterName
//	  "my-awesome-resource" // resName
//	  "APIExports", // kind
//	  apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"), // gvr
//	  &apisv1alpha1.APIExport{...}, // the resource
//	  &apisv1alpha1.APIExport{Spec: apisv1alpha1.APIExportSpec{...}}, // the resource with its spec modified
//	)
func replicateResourceNegative(ctx context.Context, t *testing.T,
	server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Name, /*cluster for hosting the provided resource, can be empty*/
	kind string, /*kind for the given resource*/
	gvr schema.GroupVersionResource, /*gvr for the given resource*/
	res runtime.Object, /*a strongly typed resource object that will be created*/
	resWithModifiedSpec runtime.Object /*a strongly typed resource obj with modified spec only, will be used for an update*/) {
	t.Helper()

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	if clusterName.Empty() {
		_, ws := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithRootShard())
		clusterName = logicalcluster.Name(ws.Spec.Cluster)
	}
	resMeta, err := meta.Accessor(res)
	require.NoError(t, err)
	resourceName := resMeta.GetName()
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: kind, gvr: gvr, cluster: clusterName, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source %s %s/%s on the root shard for replication", kind, clusterName, resourceName)
	scenario.CreateSourceResource(ctx, t, res)
	t.Logf("Verify that the source %s %s/%s was replicated to the cache server", kind, clusterName, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Delete cached %s %s/%s and check if it was brought back by the replication controller", kind, clusterName, resourceName)
	scenario.DeleteCachedResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	if resWithModifiedSpec != nil {
		t.Logf("Update cached %s %s/%s so that it differs from the source resource", kind, clusterName, scenario.resourceName)
		scenario.UpdateSpecCachedResource(ctx, t, resWithModifiedSpec)
		t.Logf("Verify that the cached %s %s/%s was brought back by the replication controller after an update", kind, clusterName, resourceName)
		scenario.VerifyReplication(ctx, t)
	}

	t.Logf("Update cached %s %s/%s so that it differs from the source resource", kind, clusterName, scenario.resourceName)
	scenario.UpdateMetaCachedResource(ctx, t)
	t.Logf("Verify that the cached %s %s/%s was brought back by the replication controller after an update", kind, clusterName, resourceName)
	scenario.VerifyReplication(ctx, t)
}

// TestReplication runs all test scenarios against a default setup possibly with the cache server running within kcp server.
func TestReplication(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpShardDynamicClient, err := kcpdynamic.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	cacheClientConfig := createCacheClientConfigForEnvironment(ctx, t, kcpRootShardConfig)
	cacheClientRT := ClientRoundTrippersFor(cacheClientConfig)
	cacheKcpClusterDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, server, kcpShardDynamicClient, cacheKcpClusterDynamicClient)
		})
	}
}

// TestReplicationDisruptive runs each disruptive test in its own private environment.
func TestReplicationDisruptive(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	for _, scenario := range disruptiveScenarios {
		scenario := scenario

		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			tokenAuthFile := framework.WriteTokenAuthFile(t)
			server := framework.PrivateKcpServer(t,
				framework.WithCustomArguments(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)...))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
			kcpRootShardDynamicClient, err := kcpdynamic.NewForConfig(kcpRootShardConfig)
			require.NoError(t, err)
			cacheClientRT := ClientRoundTrippersFor(kcpRootShardConfig)
			cacheKcpClusterDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
			require.NoError(t, err)

			scenario.work(ctx, t, server, kcpRootShardDynamicClient, cacheKcpClusterDynamicClient)
		})
	}
}

// replicateResourceScenario an auxiliary struct that is used by all test scenarios defined in this pkg.
type replicateResourceScenario struct {
	resourceName string
	cluster      logicalcluster.Name

	gvr  schema.GroupVersionResource
	kind string

	server                       framework.RunningServer
	kcpShardClusterDynamicClient kcpdynamic.ClusterInterface
	cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface
}

func (b *replicateResourceScenario) CreateSourceResource(ctx context.Context, t *testing.T, res runtime.Object) {
	t.Helper()
	resUnstructured, err := toUnstructured(res, b.kind, b.gvr)
	require.NoError(t, err)
	_, err = b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Create(ctx, resUnstructured, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (b *replicateResourceScenario) UpdateMetaSourceResource(ctx context.Context, t *testing.T) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := b.changeMetadataFor(res); err != nil {
			return err
		}
		resUnstructured, err := toUnstructured(res, b.kind, b.gvr)
		if err != nil {
			return err
		}
		_, err = b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(ctx, resUnstructured, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateSpecSourceResource(ctx context.Context, t *testing.T, resWithModifiedSpec runtime.Object) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		unstructuredResWithModSpec, err := toUnstructured(resWithModifiedSpec, b.kind, b.gvr)
		require.NoError(t, err)
		modifiedSpec, hasSpec, err := unstructured.NestedFieldCopy(unstructuredResWithModSpec.Object, "spec")
		require.NoError(t, err)
		if !hasSpec {
			return fmt.Errorf("provided resource doesn't have a spec")
		}
		actualSpec, hasSpec, err := unstructured.NestedFieldCopy(res.Object, "spec")
		if err != nil {
			return err
		}
		if !hasSpec {
			return fmt.Errorf("original resource doesn't have a spec")
		}
		newSpec := map[string]interface{}{}
		for k, v := range actualSpec.(map[string]interface{}) {
			newSpec[k] = v
		}
		for k, v := range modifiedSpec.(map[string]interface{}) {
			newSpec[k] = v
		}
		err = unstructured.SetNestedField(res.Object, newSpec, "spec")
		require.NoError(t, err)
		_, err = b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(ctx, res, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateMetaCachedResource(ctx context.Context, t *testing.T) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := b.changeMetadataFor(res); err != nil {
			return err
		}
		resUnstructured, err := toUnstructured(res, b.kind, b.gvr)
		if err != nil {
			return err
		}
		_, err = b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(cacheclient.WithShardInContext(ctx, shard.New("root")), resUnstructured, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateSpecCachedResource(ctx context.Context, t *testing.T, resWithModifiedSpec runtime.Object) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		unstructuredResWithModSpec, err := toUnstructured(resWithModifiedSpec, b.kind, b.gvr)
		require.NoError(t, err)
		modifiedSpec, hasSpec, err := unstructured.NestedFieldCopy(unstructuredResWithModSpec.Object, "spec")
		require.NoError(t, err)
		if !hasSpec {
			return fmt.Errorf("provided resource doesn't have a spec")
		}
		actualSpec, hasSpec, err := unstructured.NestedFieldCopy(res.Object, "spec")
		if err != nil {
			return err
		}
		if !hasSpec {
			return fmt.Errorf("original resource doesn't have a spec")
		}
		newSpec := map[string]interface{}{}
		for k, v := range actualSpec.(map[string]interface{}) {
			newSpec[k] = v
		}
		for k, v := range modifiedSpec.(map[string]interface{}) {
			newSpec[k] = v
		}
		err = unstructured.SetNestedField(res.Object, newSpec, "spec")
		require.NoError(t, err)
		_, err = b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(cacheclient.WithShardInContext(ctx, shard.New("root")), res, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) DeleteSourceResourceAndVerify(ctx context.Context, t *testing.T) {
	t.Helper()
	require.NoError(t, b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Delete(ctx, b.resourceName, metav1.DeleteOptions{}))
	framework.Eventually(t, func() (bool, string) {
		_, err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("replicated %s %s/%s wasn't removed", b.gvr, b.cluster, b.resourceName)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) DeleteCachedResource(ctx context.Context, t *testing.T) {
	t.Helper()
	err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.DeleteOptions{})
	require.NoError(t, err)
}

func (b *replicateResourceScenario) VerifyReplication(ctx context.Context, t *testing.T) {
	t.Helper()
	b.verifyResourceReplicationHelper(ctx, t)
}

func (b *replicateResourceScenario) changeMetadataFor(originalResource *unstructured.Unstructured) error {
	originalResourceMeta, err := meta.Accessor(originalResource)
	if err != nil {
		return err
	}
	annotations := originalResourceMeta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["testAnnotation"] = "testAnnotationValue"
	originalResourceMeta.SetAnnotations(annotations)
	return nil
}

func (b *replicateResourceScenario) resourceUpdateHelper(ctx context.Context, t *testing.T, resourceGetter func(ctx context.Context) (*unstructured.Unstructured, error), resourceUpdater func(*unstructured.Unstructured) error) {
	t.Helper()
	framework.Eventually(t, func() (bool, string) {
		resource, err := resourceGetter(ctx)
		if err != nil {
			return false, err.Error()
		}
		err = resourceUpdater(resource)
		if err != nil {
			if !errors.IsConflict(err) {
				return false, fmt.Sprintf("unknown error while updating the cached %s/%s/%s, err: %s", b.gvr, b.cluster, b.resourceName, err.Error())
			}
			return false, err.Error() // try again
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) verifyResourceReplicationHelper(ctx context.Context, t *testing.T) {
	t.Helper()
	cluster := b.cluster.Path()
	t.Logf("Get %s %s/%s from the root shard and the cache server for comparison", b.gvr, cluster, b.resourceName)
	framework.Eventually(t, func() (bool, string) {
		originalResource, err := b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cachedResource, err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				return true, err.Error()
			}
			return false, err.Error()
		}
		t.Logf("Compare if both the original and replicated resources (%s %s/%s) are the same except %s annotation and ResourceVersion", b.gvr, cluster, b.resourceName, genericapirequest.ShardAnnotationKey)
		cachedResourceMeta, err := meta.Accessor(cachedResource)
		if err != nil {
			return false, err.Error()
		}
		if _, found := cachedResourceMeta.GetAnnotations()[genericapirequest.ShardAnnotationKey]; !found {
			t.Fatalf("replicated %s root|%s/%s, doesn't have %s annotation", b.gvr, cluster, cachedResourceMeta.GetName(), genericapirequest.ShardAnnotationKey)
		}
		unstructured.RemoveNestedField(originalResource.Object, "metadata", "resourceVersion")
		unstructured.RemoveNestedField(cachedResource.Object, "metadata", "resourceVersion")

		// TODO(davidfestal): find out why the generation is not equal, specially for rbacv1.
		// Is it a characteristic of all built-in KCP resources (which are not backed by CRDs) ?
		// Issue opened: https://github.com/kcp-dev/kcp/issues/2935
		if b.gvr.Group == rbacv1.SchemeGroupVersion.Group {
			unstructured.RemoveNestedField(originalResource.Object, "metadata", "generation")
			unstructured.RemoveNestedField(cachedResource.Object, "metadata", "generation")
		}

		unstructured.RemoveNestedField(cachedResource.Object, "metadata", "annotations", genericapirequest.ShardAnnotationKey)
		if cachedStatus, ok := cachedResource.Object["status"]; ok && cachedStatus == nil || (cachedStatus != nil && len(cachedStatus.(map[string]interface{})) == 0) {
			// TODO: worth investigating:
			// for some reason cached resources have an empty status set whereas the original resources don't
			unstructured.RemoveNestedField(cachedResource.Object, "status")
		}
		if diff := cmp.Diff(cachedResource.Object, originalResource.Object); len(diff) > 0 {
			return false, fmt.Sprintf("replicated %s root|%s/%s is different from the original: %s", b.gvr, cluster, cachedResourceMeta.GetName(), diff)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func toUnstructured(obj interface{}, kind string, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	unstructured.SetKind(kind)
	unstructured.SetAPIVersion(gvr.GroupVersion().String())
	return unstructured, nil
}

func withPseudoRandomSuffix(name string) string {
	return fmt.Sprintf("%s-%d", name, rand.Int())
}

// createCacheClientConfigForEnvironment is a helper function
// for creating a rest config for the cache server depending on
// the underlying test environment.
func createCacheClientConfigForEnvironment(ctx context.Context, t *testing.T, kcpRootShardConfig *rest.Config) *rest.Config {
	// TODO: in the future we might associate a shard instance with a cache server
	// via some field on Shard resources, in that case we could read the value of
	// that field for creating a rest config.
	t.Helper()
	kcpRootShardClient, err := kcpclientset.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	shards, err := kcpRootShardClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	if len(shards.Items) == 1 {
		// assume single shard env with embedded cache server
		return kcpRootShardConfig
	}

	// assume multi-shard env created by the sharded-test-server
	cacheServerKubeConfigPath := filepath.Join(framework.RepositoryDir(), ".kcp-cache", "cache.kubeconfig")
	cacheServerKubeConfig, err := framework.LoadKubeConfig(cacheServerKubeConfigPath, "cache")
	require.NoError(t, err)
	cacheServerRestConfig, err := cacheServerKubeConfig.ClientConfig()
	require.NoError(t, err)
	return cacheServerRestConfig
}
