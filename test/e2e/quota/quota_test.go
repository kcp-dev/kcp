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

package quota

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestKubeQuotaBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.DefaultConfig(t)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgClusterName := framework.NewOrganizationFixture(t, server)

	// Create more than 1 workspace with the same quota restrictions to validate that after we create the first workspace
	// and fill its quota to capacity, subsequent workspaces have independent quota.
	for i := 0; i < 3; i++ {
		t.Logf("Creating workspace %d", i)
		ws := framework.NewWorkspaceFixture(t, server, orgClusterName)

		ws1Quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name: "quota",
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					"count/configmaps": resource.MustParse("2"),
				},
			},
		}

		t.Logf("Creating ws quota")
		ws1Quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Create(ctx, ws1Quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating ws quota")

		t.Logf("Waiting for ws quota to show used configmaps (kube-root-ca.crt)")
		require.Eventually(t, func() bool {
			ws1Quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, "quota", metav1.GetOptions{})
			if err != nil {
				t.Logf("Error getting ws quota %s|default/quota: %v", ws, err)
			}

			used := ws1Quota.Status.Used["count/configmaps"]
			return used.Equal(resource.MustParse("1"))
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 1 used configmaps, ws quota is %#v", ws1Quota)

		ws1ConfigMap1 := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm1"}}
		t.Logf("Creating ws ConfigMap cm1")
		_, err = kubeClusterClient.Cluster(ws).CoreV1().ConfigMaps("default").Create(ctx, ws1ConfigMap1, metav1.CreateOptions{})
		require.NoError(t, err, "error creating ws ConfigMap cm1")

		t.Logf("Waiting for ws quota to show 2 used configmaps")
		require.Eventually(t, func() bool {
			ws1Quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, "quota", metav1.GetOptions{})
			if err != nil {
				t.Logf("Error getting ws quota %s|default/quota: %v", ws, err)
			}

			used := ws1Quota.Status.Used["count/configmaps"]
			return used.Equal(resource.MustParse("2"))
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 2 used configmaps, ws quota is %#v", ws1Quota)

		ws1ConfigMap2 := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm2"}}
		t.Logf("Creating ws ConfigMap cm2 - expect quota rejection")
		_, err = kubeClusterClient.Cluster(ws).CoreV1().ConfigMaps("default").Create(ctx, ws1ConfigMap2, metav1.CreateOptions{})
		require.Error(t, err, "expected error creating ws ConfigMap cm2")
	}
}

func TestKubeQuotaCoreV1TypesFromBinding(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	negotiationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	workloadClusterName := fmt.Sprintf("workloadcluster-%d", +rand.Intn(1000000))
	t.Logf("Creating a WorkloadCluster and syncer in %s", negotiationClusterName)
	_ = framework.SyncerFixture{
		ResourcesToSync:      sets.NewString("services"),
		UpstreamServer:       source,
		WorkspaceClusterName: negotiationClusterName,
		WorkloadClusterName:  workloadClusterName,
		InstallCRDs: func(config *rest.Config, isLogicalCluster bool) {
			if !isLogicalCluster {
				// Only need to install in a logical cluster pretending to be a real one
				return
			}

			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
			)
			require.NoError(t, err)
		},
	}.Start(t)

	t.Logf("Wait for APIResourceImports to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		imports, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().APIResourceImports().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceImports: %v", err)
			return false
		}

		return len(imports.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for NegotiatedAPIResources to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().NegotiatedAPIResources().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list NegotiatedAPIResources: %v", err)
			return false
		}

		return len(resources.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apiexport")
	var export *apisv1alpha1.APIExport
	require.Eventually(t, func() bool {
		export, err = kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Get(ctx, "kubernetes", metav1.GetOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apibinding that is bound")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIBindings().Get(ctx, "kubernetes", metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return false, ""
		}
		return binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound, toYaml(binding)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		schemas, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIResourceSchemas().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceSchemas: %v", err)
			return false
		}

		return len(schemas.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the APIExport spec")
	require.Eventually(t, func() bool {
		export, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Get(ctx, export.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return len(export.Spec.LatestResourceSchemas) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubernetes",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       negotiationClusterName.String(),
					ExportName: "kubernetes",
				},
			},
		},
	}

	t.Logf("Create a binding in the user workspace")
	_, err = kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list Locations: %v", err)
		}
		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create quota in user workspace")
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name: "quota",
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: map[corev1.ResourceName]resource.Quantity{
				"count/services": resource.MustParse("1"),
			},
		},
	}

	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().ResourceQuotas("default").Create(ctx, quota, metav1.CreateOptions{})
	require.NoError(t, err, "error creating quota")

	t.Logf("Waiting for quota to show 0 used Services")
	framework.Eventually(t, func() (bool, string) {
		quota, err = kubeClusterClient.Cluster(userClusterName).CoreV1().ResourceQuotas("default").Get(ctx, "quota", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error getting ws quota %s|default/quota: %v", userClusterName, err)
		}

		used := quota.Status.Used["count/services"]
		return used.Equal(resource.MustParse("0")), used.String()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 0 used Services")

	service1 := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "service1"}}
	t.Logf("Creating Service service1")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("default").Create(ctx, service1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating Service service1")

	t.Logf("Waiting for quota to show 1 used Service")
	framework.Eventually(t, func() (bool, string) {
		quota, err = kubeClusterClient.Cluster(userClusterName).CoreV1().ResourceQuotas("default").Get(ctx, "quota", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error getting ws quota %s|default/quota: %v", userClusterName, err)
		}

		used := quota.Status.Used["count/services"]
		return used.Equal(resource.MustParse("1")), used.String()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 0 used Services")

	service2 := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "service2"}}
	t.Logf("Creating Service service2 - expect quota rejection")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("default").Create(ctx, service2, metav1.CreateOptions{})
	require.Error(t, err, "expected error creating Service service1")
}

func TestKubeQuotaNormalCRDs(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.DefaultConfig(t)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgClusterName := framework.NewOrganizationFixture(t, server)

	group := uuid.New().String() + ".io"

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	ws1 := framework.NewWorkspaceFixture(t, server, orgClusterName)
	ws2 := framework.NewWorkspaceFixture(t, server, orgClusterName)

	t.Logf("Install a normal sheriffs CRD into workspace 1 %q", ws1)
	bootstrapCRD(t, ws1, crdClusterClient.Cluster(ws1).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD with a different schema into workspace 2 %q", ws2)
	bootstrapCRD(t, ws2, crdClusterClient.Cluster(ws2).ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	sheriffsObjectCountName := corev1.ResourceName("count/sheriffs." + group)

	workspaces := []logicalcluster.Name{ws1, ws2}
	for i, ws := range workspaces {
		wsIndex := i + 1
		quotaName := group

		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name: quotaName,
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					sheriffsObjectCountName: resource.MustParse("2"),
				},
			},
		}

		t.Logf("Creating ws %d quota", wsIndex)
		quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Create(ctx, quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating ws %d quota", wsIndex)

		t.Logf("Waiting for ws %d quota to show usage", wsIndex)
		require.Eventually(t, func() bool {
			quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, quotaName, metav1.GetOptions{})
			if err != nil {
				t.Logf("Error getting ws %d quota %s|default/quota: %v", wsIndex, ws, err)
			}

			used, ok := quota.Status.Used[sheriffsObjectCountName]
			return ok && used.Equal(resource.MustParse("0"))
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 2 used, ws %d quota is %#v", wsIndex, quota)

		apifixtures.EventuallyCreateSheriff(ctx, t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-1", wsIndex))
		apifixtures.EventuallyCreateSheriff(ctx, t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-2", wsIndex))

		t.Logf("Waiting for ws %d quota to show 2 used", wsIndex)
		require.Eventually(t, func() bool {
			quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, quotaName, metav1.GetOptions{})
			if err != nil {
				t.Logf("Error getting ws %d quota %s|default/quota: %v", wsIndex, ws, err)
			}

			used := quota.Status.Used[sheriffsObjectCountName]
			return used.Equal(resource.MustParse("2"))
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 2 used, ws %d quota is %#v", wsIndex, quota)

		apifixtures.CreateSheriffAndExpectError(ctx, t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-3", wsIndex))
	}
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

func toYaml(obj interface{}) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}
