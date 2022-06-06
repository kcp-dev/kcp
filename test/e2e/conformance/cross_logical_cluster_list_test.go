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

package conformance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/informer"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.DefaultConfig(t)

	logicalClusters := []logicalcluster.Name{
		framework.NewOrganizationFixture(t, server),
		framework.NewOrganizationFixture(t, server),
	}
	expectedWorkspaces := sets.NewString()
	for i, logicalCluster := range logicalClusters {
		kcpClients, err := kcpclientset.NewClusterForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp client for server")

		wsName := fmt.Sprintf("ws-%d", i)

		t.Logf("Creating ClusterWorkspace CRs in logical cluster %s", logicalCluster)
		kcpClient := kcpClients.Cluster(logicalCluster)
		sourceWorkspace := &tenancyapi.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: wsName,
			},
		}
		_, err = kcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
		require.NoError(t, err, "error creating source workspace")

		expectedWorkspaces.Insert(logicalCluster.Join(wsName).String())

		server.Artifact(t, func() (runtime.Object, error) {
			return kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
		})
	}

	t.Logf("Listing ClusterWorkspace CRs across logical clusters")
	kcpClients, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp client for server")
	kcpClient := kcpClients.Cluster(logicalcluster.Wildcard)
	workspaces, err := kcpClient.TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing workspaces")

	t.Logf("Expecting at least those ClusterWorkspaces we created above")
	got := sets.NewString()
	for _, ws := range workspaces.Items {
		got.Insert(logicalcluster.From(&ws).Join(ws.Name).String())
	}
	require.True(t, got.IsSuperset(expectedWorkspaces), "unexpected workspaces detected")
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Name,
	client apiextensionsv1client.CustomResourceDefinitionInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	t.Cleanup(cancelFunc)

	err := configcrds.CreateSingle(ctx, client, crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}

// ensure PartialObjectMetadata wildcard list works even with different CRD schemas
func TestCRDCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	org := framework.NewOrganizationFixture(t, server)

	// These 2 workspaces will have the same sheriffs CRD schema as normal CRDs
	wsNormalCRD1a := framework.NewWorkspaceFixture(t, server, org, "Universal")
	wsNormalCRD1b := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// This workspace will have a different sherrifs CRD schema as a normal CRD - will conflict with 1a/1b.
	wsNormalCRD2 := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// These 2 workspaces will export a sheriffs API with the same schema
	wsExport1a := framework.NewWorkspaceFixture(t, server, org, "Universal")
	wsExport1b := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// This workspace will export a sheriffs API with a different schema
	wsExport2 := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// This workspace will consume from wsExport1a
	wsConsume1a := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// This workspace will consume from wsExport1b
	wsConsume1b := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// This workspace will consume from wsExport2
	wsConsume2 := framework.NewWorkspaceFixture(t, server, org, "Universal")

	// Make sure the informers aren't throttled because dynamic informers do lots of discovery which slows down tests
	cfg := server.DefaultConfig(t)
	cfg.QPS = 500
	cfg.Burst = 1000

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	group := uuid.New().String() + ".io"

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	sheriffsGVR := schema.GroupVersionResource{Group: sheriffCRD1.Spec.Group, Resource: "sheriffs", Version: "v1"}

	t.Logf("Install a normal sheriffs CRD into workspace %q", wsNormalCRD1a)
	bootstrapCRD(t, wsNormalCRD1a, crdClusterClient.Cluster(wsNormalCRD1a).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD into workspace %q", wsNormalCRD1b)
	bootstrapCRD(t, wsNormalCRD1b, crdClusterClient.Cluster(wsNormalCRD1b).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Trying to wildcard list")
	_, err = dynamicClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work because schemas are the same")

	t.Logf("Install a different sheriffs CRD into workspace %q", wsNormalCRD2)
	bootstrapCRD(t, wsNormalCRD2, crdClusterClient.Cluster(wsNormalCRD2).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	t.Logf("Trying to wildcard list and expecting it to fail now")
	require.Eventually(t, func() bool {
		_, err = dynamicClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		return err != nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected wildcard list to fail because schemas are different")

	kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1a, group, wsNormalCRD1a.String())
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1b, group, wsNormalCRD1b.String())

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1a, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1a, group, wsConsume1a, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsConsume1a, group, wsConsume1a.String())

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1b, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1b, group, wsConsume1b, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsConsume1b, group, wsConsume1b.String())

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport2, kcpClusterClient, group, "export2")
	apifixtures.BindToExport(ctx, t, wsExport2, group, wsConsume2, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsConsume2, group, wsConsume2.String())

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")
	_, err = metadataClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work with metadata client even though schemas are different")

	t.Log("Start dynamic metadata informers")
	kcpInformer := kcpinformers.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(logicalcluster.Wildcard), 0)
	kcpInformer.Tenancy().V1alpha1().ClusterWorkspaces().Lister()
	kcpInformer.Start(ctx.Done())
	kcpInformer.WaitForCacheSync(ctx.Done())
	require.NoError(t, err, "failed to construct discovery client for server")
	informerFactory := informer.NewDynamicDiscoverySharedInformerFactory(
		kcpInformer.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		kcpClusterClient.DiscoveryClient,
		metadataClusterClient.Cluster(logicalcluster.Wildcard),
		func(obj interface{}) bool { return true },
		informer.GVREventHandlerFuncs{},
		time.Second*2,
	)
	informerFactory.Start(ctx)

	t.Logf("Wait for the sheriff to show up in the informer")
	// key := "default/" + clusters.ToClusterAwareKey(wsNormalCRD1a, "john-hicks-adams")
	require.Eventually(t, func() bool {
		listers, _ := informerFactory.Listers()

		lister := listers[sheriffsGVR]
		if lister == nil {
			t.Logf("Waiting for sheriffs to show up in dynamic informer")
			return false
		}

		l, err := lister.List(labels.Everything())
		if err != nil {
			t.Logf("Error listing sheriffs: %v", err)
			return false
		}

		return len(l) == 5
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected 5 sheriffs to show up in informer")
}

func TestBuiltInCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	org := framework.NewOrganizationFixture(t, server)

	cfg := server.DefaultConfig(t)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	for i := 0; i < 3; i++ {
		ws := framework.NewWorkspaceFixture(t, server, org, "Universal")

		configMapName := fmt.Sprintf("test-cm-%d", i)
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: configMapName,
			},
		}

		t.Logf("Creating configmap %s|default/%s", ws, configMapName)
		_, err = kubeClusterClient.Cluster(ws).CoreV1().ConfigMaps("default").Create(ctx, configMap, metav1.CreateOptions{})
		require.NoError(t, err, "error creating configmap %s", configMapName)
	}

	configMapGVR := corev1.Resource("configmaps").WithVersion("v1")

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")
	list, err := metadataClusterClient.Cluster(logicalcluster.Wildcard).Resource(configMapGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work")

	names := sets.NewString()
	for i := range list.Items {
		names.Insert(list.Items[i].GetName())
	}

	expected := []string{"test-cm-0", "test-cm-1", "test-cm-2"}

	require.Subset(t, names.List(), expected)
}
