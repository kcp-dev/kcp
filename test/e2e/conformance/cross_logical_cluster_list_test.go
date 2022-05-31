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
	"encoding/json"
	"fmt"
	"strings"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
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

func newCRDWithSchemaDescription(group, description string) *apiextensionsv1.CustomResourceDefinition {
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

func newAPIResourceSchemaWithDescription(group, description string) *apisv1alpha1.APIResourceSchema {
	name := fmt.Sprintf("today.sheriffs.%s", group)

	ret := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
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
						Raw: jsonOrDie(
							&apiextensionsv1.JSONSchemaProps{
								Type:        "object",
								Description: description,
							},
						),
					},
				},
			},
		},
	}

	return ret
}

func newAPIExport(schemaName string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{schemaName},
		},
	}
}

func jsonOrDie(obj interface{}) []byte {
	ret, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return ret
}

func exportSchema(
	ctx context.Context,
	t *testing.T,
	clusterName logicalcluster.Name,
	clusterClient kcpclientset.ClusterInterface,
	group string,
	description string,
) {
	schema := newAPIResourceSchemaWithDescription(group, description)
	t.Logf("Creating APIResourceSchema %s|%s", clusterName, schema.Name)
	_, err := clusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIResourceSchema %s|%s", clusterName, schema.Name)

	export := newAPIExport(schema.Name)
	t.Logf("Creating APIExport %s|%s", clusterName, export.Name)
	_, err = clusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport %s|%s", clusterName, export.Name)
}

func bindToExport(
	ctx context.Context,
	t *testing.T,
	exportClusterName logicalcluster.Name,
	bindingClusterName logicalcluster.Name,
	clusterClient kcpclientset.ClusterInterface,
) {
	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.Replace(exportClusterName.String(), ":", "-", -1),
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       exportClusterName.String(),
					ExportName: "sheriffs",
				},
			},
		},
	}

	t.Logf("Creating APIBinding %s|%s", bindingClusterName, binding.Name)
	_, err := clusterClient.Cluster(bindingClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIBinding %s|%s", bindingClusterName, binding.Name)

	require.Eventually(t, func() bool {
		b, err := clusterClient.Cluster(bindingClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			t.Logf("Unexpected error getting APIBinding %s|%s: %v", bindingClusterName, binding.Name, err)
			return false
		}

		return conditions.IsTrue(b, apisv1alpha1.InitialBindingCompleted)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func createSheriff(ctx context.Context, t *testing.T, dynamicClusterClient dynamic.ClusterInterface, clusterName logicalcluster.Name, group, name string) {
	name = strings.Replace(name, ":", "-", -1)

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	_, err := dynamicClusterClient.Cluster(clusterName).Resource(sheriffsGVR).Namespace("default").Create(ctx, &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": group + "/v1",
			"kind":       "Sheriff",
			"metadata": map[string]interface{}{
				"name": name,
			},
		},
	}, metav1.CreateOptions{})

	require.NoError(t, err, "failed to create sheriff %s|default/%s", clusterName, name)
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

	sheriffCRD1 := newCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := newCRDWithSchemaDescription(group, "two")

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

	createSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1a, group, wsNormalCRD1a.String())
	createSheriff(ctx, t, dynamicClusterClient, wsNormalCRD1b, group, wsNormalCRD1b.String())

	exportSchema(ctx, t, wsExport1a, kcpClusterClient, group, "export1")
	bindToExport(ctx, t, wsExport1a, wsConsume1a, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume1a, group, wsConsume1a.String())

	exportSchema(ctx, t, wsExport1b, kcpClusterClient, group, "export1")
	bindToExport(ctx, t, wsExport1b, wsConsume1b, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume1b, group, wsConsume1b.String())

	exportSchema(ctx, t, wsExport2, kcpClusterClient, group, "export2")
	bindToExport(ctx, t, wsExport2, wsConsume2, kcpClusterClient)
	createSheriff(ctx, t, dynamicClusterClient, wsConsume2, group, wsConsume2.String())

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
