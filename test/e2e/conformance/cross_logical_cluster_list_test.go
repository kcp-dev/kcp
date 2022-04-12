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
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clusters"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/informer"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.DefaultConfig(t)

	logicalClusters := []logicalcluster.LogicalCluster{
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

func TestCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	// ensure PartialObjectMetadata wildcard list works even with different CRD schemas
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	org := framework.NewOrganizationFixture(t, server)
	w1 := framework.NewWorkspaceFixture(t, server, org, "Universal")
	w2 := framework.NewWorkspaceFixture(t, server, org, "Universal")
	w3 := framework.NewWorkspaceFixture(t, server, org, "Universal")

	cfg := server.DefaultConfig(t)

	// Make sure the informers aren't throttled because dynamic informers do lots of discovery which slows down tests
	cfg.QPS = 500
	cfg.Burst = 1000

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")
	dynamicClusterClient, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	group := uuid.New().String() + ".io"

	newCRDWithSchemaDescription := func(description string) *apiextensionsv1.CustomResourceDefinition {
		crdName := fmt.Sprintf("sheriffs.%s", group)

		crd := &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: group,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "sheriffs",
					Singular: "sheriff",
					Kind:     "Sheriff",
					ListKind: "SheriffList",
				},
				Scope: "Namespaced",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type:        "object",
								Description: description,
							},
						},
					},
				},
			},
		}

		return crd
	}

	sheriffCRD1 := newCRDWithSchemaDescription("one")
	sheriffCRD2 := newCRDWithSchemaDescription("two")

	sheriffsGVR := schema.GroupVersionResource{Group: sheriffCRD1.Spec.Group, Resource: "sheriffs", Version: "v1"}

	bootstrapCRD := func(
		t *testing.T,
		clusterName logicalcluster.LogicalCluster,
		client apiextensionsv1client.CustomResourceDefinitionInterface,
		crd *apiextensionsv1.CustomResourceDefinition,
	) {
		ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
		t.Cleanup(cancelFunc)

		err = configcrds.CreateSingle(ctx, client, crd)
		require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
	}

	t.Logf("Install a normal sheriffs CRD into workspace %q", w1)
	bootstrapCRD(t, w1, crdClusterClient.Cluster(w1).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD into workspace %q", w2)
	bootstrapCRD(t, w2, crdClusterClient.Cluster(w2).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Trying to wildcard list")
	_, err = dynamicClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work because schemas are the same")

	t.Logf("Install a different sheriffs CRD into workspace %q", w3)
	bootstrapCRD(t, w3, crdClusterClient.Cluster(w3).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	t.Logf("Trying to wildcard list and expecting it to fail now")
	require.Eventually(t, func() bool {
		_, err = dynamicClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		return err != nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected wildcard list to fail because schemas are different")

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")
	_, err = metadataClusterClient.Cluster(logicalcluster.Wildcard).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work with metadata client even though schemas are different")

	t.Logf("Create a sheriff")
	_, err = dynamicClusterClient.Cluster(w1).Resource(sheriffsGVR).Namespace("default").Create(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sheriffCRD1.Spec.Group + "/v1",
			"kind":       "Sheriff",
			"metadata": map[string]interface{}{
				"name": "john-hicks-adams",
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create sheriff")

	t.Log("Start dynamic metadata informers")
	kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp client for server")
	kcpInformer := kcpinformers.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(logicalcluster.Wildcard), time.Second*30)
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
	key := "default/" + clusters.ToClusterAwareKey(w1, "john-hicks-adams")
	require.Eventually(t, func() bool {
		listers, _ := informerFactory.Listers()

		lister := listers[sheriffsGVR]
		if lister == nil {
			t.Logf("Waiting for sheriffs to show up in dynamic informer")
			return false
		}

		_, err := lister.Get(key)
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected %s sheriff %q to show up in informer", sheriffCRD1.Spec.Group, key)
}
