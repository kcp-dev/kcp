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

package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpapiextensionsinformers "github.com/kcp-dev/client-go/apiextensions/informers"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/informer"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.BaseConfig(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp client for server")

	// Note: we put all consumer workspaces onto root shard in order to enforce conflicts.
	_, ws1 := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithRootShard(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	_, ws2 := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithRootShard(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	logicalClusters := []logicalcluster.Name{
		logicalcluster.Name(ws1.Spec.Cluster),
		logicalcluster.Name(ws2.Spec.Cluster),
	}
	expectedWorkspaces := sets.New[string]()
	for i, clusterName := range logicalClusters {
		t.Logf("Creating Workspace CRs in logical cluster %s", clusterName)
		sourceWorkspace := &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ws-%d", i),
			},
		}
		ws, err := kcpClusterClient.Cluster(clusterName.Path()).TenancyV1alpha1().Workspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
		require.NoError(t, err, "error creating source workspace")

		expectedWorkspaces.Insert(logicalcluster.From(ws).String())
		server.Artifact(t, func() (runtime.Object, error) {
			obj, err := kcpClusterClient.Cluster(clusterName.Path()).TenancyV1alpha1().Workspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
			return obj, err
		})
	}

	t.Log("Listing Workspace CRs across logical clusters with identity")
	tenancyExport, err := kcpClusterClient.ApisV1alpha2().APIExports().Cluster(core.RootCluster.Path()).Get(ctx, "tenancy.kcp.io", metav1.GetOptions{})
	require.NoError(t, err, "error getting tenancy API export")
	require.NotEmptyf(t, tenancyExport.Status.IdentityHash, "tenancy API export has no identity hash")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(rootShardCfg)
	require.NoError(t, err, "failed to construct kcp client for server")
	client := dynamicClusterClient.Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource(fmt.Sprintf("workspaces:%s", tenancyExport.Status.IdentityHash)))
	workspaces, err := client.List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing workspaces")
	got := sets.New[string]()
	for _, ws := range workspaces.Items {
		got.Insert(logicalcluster.From(&ws).String())
	}
	require.True(t, got.IsSuperset(expectedWorkspaces), "unexpected workspaces detected, got: %v, expected: %v", sets.List[string](got), sets.List[string](expectedWorkspaces))
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Path,
	clusterClient kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	t.Helper()

	ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	t.Cleanup(cancelFunc)

	err := configcrds.CreateSingle(ctx, clusterClient.Cluster(clusterName), crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}

// ensure PartialObjectMetadata wildcard list works even with different CRD schemas.
func TestCRDCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	t.Parallel()

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Note: we put all consumer workspaces onto root shard in order to enforce conflicts.

	// These 2 workspaces will have the same sheriffs CRD schema as normal CRDs
	wsNormalCRD1a, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())
	wsNormalCRD1b, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	// This workspace will have a different sherrifs CRD schema as a normal CRD - will conflict with 1a/1b.
	wsNormalCRD2, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	// These 2 workspaces will export a sheriffs API with the same schema
	wsExport1a, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	wsExport1b, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// This workspace will export a sheriffs API with a different schema
	wsExport2, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// This workspace will consume from wsExport1a
	wsConsume1a, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	// This workspace will consume from wsExport1b
	wsConsume1b, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	// This workspace will consume from wsExport2
	wsConsume2, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	cfg := server.BaseConfig(t)
	rootShardConfig := server.RootShardSystemMasterBaseConfig(t)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp client for server")

	group := framework.UniqueGroup(".io")

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	sheriffsGVR := schema.GroupVersionResource{Group: sheriffCRD1.Spec.Group, Resource: "sheriffs", Version: "v1"}

	t.Logf("Install a normal sheriffs CRD into workspace %q", wsNormalCRD1a)
	bootstrapCRD(t, wsNormalCRD1a, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD into workspace %q", wsNormalCRD1b)
	bootstrapCRD(t, wsNormalCRD1b, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Create a root shard client that is able to do wildcard requests")
	rootShardDynamicClients, err := kcpdynamic.NewForConfig(rootShardConfig)
	require.NoError(t, err)

	t.Logf("Trying to wildcard list without identity. It should fail.")
	_, err = rootShardDynamicClients.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.Error(t, err, "expected wildcard list to fail because CRD have no identity cross-workspace")

	t.Logf("Install a different sheriffs CRD into workspace %q", wsNormalCRD2)
	bootstrapCRD(t, wsNormalCRD2, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	createSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1a, group)
	createSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1b, group)

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1a, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1a, group, wsConsume1a, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume1a, group)

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1b, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1b, group, wsConsume1b, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume1b, group)

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport2, kcpClusterClient, group, "export2")
	apifixtures.BindToExport(ctx, t, wsExport2, group, wsConsume2, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume2, group)

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	rootShardMetadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(rootShardConfig)
	require.NoError(t, err, "failed to construct dynamic client for server")
	_, err = rootShardMetadataClusterClient.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work with metadata client even though schemas are different")

	rootShardCRDClusterClient, err := kcpapiextensionsclientset.NewForConfig(rootShardConfig)
	require.NoError(t, err, "error creating root shard crd client")

	apiExtensionsInformerFactory := kcpapiextensionsinformers.NewSharedInformerFactoryWithOptions(
		rootShardCRDClusterClient,
		0,
	)

	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(
		rest.AddUserAgent(rest.CopyConfig(rootShardConfig), "kcp-partial-metadata-informers"))
	require.NoError(t, err, "error creating metadata cluster client")

	crdGVRSource, err := informer.NewCRDGVRSource(apiExtensionsInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer())
	require.NoError(t, err, "error creating CRD-based GVR source")

	informerFactory, err := informer.NewDiscoveringDynamicSharedInformerFactory(
		metadataClusterClient,
		func(obj interface{}) bool { return true },
		nil,
		crdGVRSource,
		cache.Indexers{},
	)
	require.NoError(t, err, "error creating DynamicDiscoverySharedInformerFactory")

	// Have to start this after informer.NewDynamicDiscoverySharedInformerFactory() is invoked, as that adds an
	// index to the crd informer that is required for the dynamic factory to work correctly.
	t.Log("Start apiextensions informers")
	apiExtensionsInformerFactory.Start(ctx.Done())
	cacheSyncCtx, cacheSyncCancel := context.WithTimeout(ctx, wait.ForeverTestTimeout)
	t.Cleanup(cacheSyncCancel)
	apiExtensionsInformerFactory.WaitForCacheSync(cacheSyncCtx.Done())

	t.Log("Start dynamic metadata informers")
	go informerFactory.StartWorker(ctx)

	t.Logf("Wait for the sheriff to show up in the informer")
	// key := "default/" + client.ToClusterAwareKey(wsNormalCRD1a, "john-hicks-adams")
	require.Eventually(t, func() bool {
		informers, _ := informerFactory.Informers()

		informer := informers[sheriffsGVR]
		if informer == nil {
			t.Logf("Waiting for sheriffs to show up in dynamic informer")
			return false
		}

		l, err := informer.Lister().List(labels.Everything())
		if err != nil {
			t.Logf("Error listing sheriffs: %v", err)
			return false
		}

		return len(l) == 5
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected 5 sheriffs to show up in informer")
}

func TestBuiltInCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	t.Parallel()

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	cfg := server.BaseConfig(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	for i := range 3 {
		wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

		configMapName := fmt.Sprintf("test-cm-%d", i)
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapName,
			},
		}

		t.Logf("Creating configmap %s|default/%s", wsPath, configMapName)
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(ctx, configMap, metav1.CreateOptions{})
		require.NoError(t, err, "error creating configmap %s", configMapName)
	}

	configMapGVR := corev1.Resource("configmaps").WithVersion("v1")

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(rootShardCfg)
	require.NoError(t, err, "failed to construct dynamic client for server")
	list, err := metadataClusterClient.Resource(configMapGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work")

	names := sets.New[string]()
	for i := range list.Items {
		names.Insert(list.Items[i].GetName())
	}

	expected := []string{"test-cm-0", "test-cm-1", "test-cm-2"}

	require.Subset(t, sets.List[string](names), expected)
}

func createSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Path,
	group string,
) {
	t.Helper()

	name := strings.ReplaceAll(clusterName.String(), ":", "-")

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	sheriff := &unstructured.Unstructured{}
	sheriff.SetAPIVersion(group + "/v1")
	sheriff.SetKind("Sheriff")
	sheriff.SetName(name)

	// CRDs are asynchronously served because they are informer based.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		if _, err := dynamicClusterClient.Cluster(clusterName).Resource(sheriffsGVR).Namespace("default").Create(ctx, sheriff, metav1.CreateOptions{}); err != nil {
			return false, fmt.Sprintf("failed to create Sheriff %s|%s: %v", clusterName, name, err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "error creating Sheriff %s|%s", clusterName, name)
}
