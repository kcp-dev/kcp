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
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	kcpapiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestKubeQuotaBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgClusterName := framework.NewOrganizationFixture(t, server)

	// Create more than 1 workspace with the same quota restrictions to validate that after we create the first workspace
	// and fill its quota to capacity, subsequent workspaces have independent quota.
	for i := 0; i < 3; i++ {
		ws := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("quota-%d", i))

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
		framework.Eventually(t, func() (bool, string) {
			ws1Quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, "quota", metav1.GetOptions{})
			require.NoError(t, err, "Error getting ws quota %s|default/quota: %v", ws, err)

			used, ok := ws1Quota.Status.Used["count/configmaps"]
			return ok && used.Equal(resource.MustParse("1")), fmt.Sprintf("ok=%t, used=%s", ok, used.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 1 used configmaps")

		t.Logf("Make sure quota is enforcing limits")
		framework.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a configmap")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
			_, err = kubeClusterClient.Cluster(ws).CoreV1().ConfigMaps("default").Create(ctx, cm, metav1.CreateOptions{})
			return apierrors.IsForbidden(err), fmt.Sprintf("%v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected configmap creation")
	}
}

func TestKubeQuotaCoreV1TypesFromBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Test multiple workspaces in parallel
	for i := 0; i < 5; i++ {
		t.Run(fmt.Sprintf("tc%d", i), func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(ctx)
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, source)
			apiProviderClustername := framework.NewWorkspaceFixture(t, source, orgClusterName, framework.WithName("api-provider"))
			userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, framework.WithName("user"))

			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
			require.NoError(t, err)
			kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
			require.NoError(t, err)

			t.Logf("Check that there is no services resource in the user workspace")
			_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
			require.Error(t, err)

			t.Logf("Getting services CRD")
			servicesCRD := kubefixtures.CRD(t, metav1.GroupResource{Group: "core.k8s.io", Resource: "services"})

			t.Logf("Converting services CRD to APIResourceSchema")
			servicesAPIResourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(servicesCRD, "some-prefix")
			require.NoError(t, err, "error converting CRD to APIResourceSchema")

			t.Logf("Creating APIResourceSchema")
			_, err = kcpClusterClient.Cluster(apiProviderClustername).ApisV1alpha1().APIResourceSchemas().Create(ctx, servicesAPIResourceSchema, metav1.CreateOptions{})
			require.NoError(t, err, "error creating APIResourceSchema")

			t.Logf("Creating APIExport")
			servicesAPIExport := &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "services",
				},
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{
						servicesAPIResourceSchema.Name,
					},
				},
			}

			_, err = kcpClusterClient.Cluster(apiProviderClustername).ApisV1alpha1().APIExports().Create(ctx, servicesAPIExport, metav1.CreateOptions{})
			require.NoError(t, err, "error creating APIExport")

			t.Logf("Create a binding in the user workspace")
			binding := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "services",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							Path:       apiProviderClustername.String(),
							ExportName: servicesAPIExport.Name,
						},
					},
				},
			}

			_, err = kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("Wait for binding to be ready")
			framework.Eventually(t, func() (bool, string) {
				binding, err := kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
				require.NoError(t, err, "error getting binding %s", binding.Name)
				condition := conditions.Get(binding, apisv1alpha1.InitialBindingCompleted)
				if condition == nil {
					return false, fmt.Sprintf("no %s condition exists", apisv1alpha1.InitialBindingCompleted)
				}
				if condition.Status == corev1.ConditionTrue {
					return true, ""
				}
				return false, fmt.Sprintf("not done waiting for the binding to be initially bound, reason: %v - message: %v", condition.Reason, condition.Message)
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("Wait for being able to list Services in the user workspace")
			framework.Eventually(t, func() (bool, string) {
				_, err := kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
				if err != nil {
					return false, fmt.Sprintf("Failed to list Services: %v", err)
				}
				return true, ""
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
				require.NoError(t, err, "Error getting ws quota %s|default/quota: %v", userClusterName, err)

				used, ok := quota.Status.Used["count/services"]
				return ok && used.Equal(resource.MustParse("0")), used.String()
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 0 used Services")

			t.Logf("Make sure quota is enforcing limits")
			framework.Eventually(t, func() (bool, string) {
				t.Logf("Trying to create a service")
				service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
				_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("default").Create(ctx, service, metav1.CreateOptions{})
				return apierrors.IsForbidden(err), fmt.Sprintf("%v", err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected service creation")
		})
	}
}

func TestKubeQuotaNormalCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgClusterName := framework.NewOrganizationFixture(t, server)

	group := framework.UniqueGroup(".io")

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	ws1 := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("one"))
	ws2 := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("two"))

	t.Logf("Install a normal sheriffs CRD into workspace 1 %q", ws1)
	bootstrapCRD(t, ws1, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD with a different schema into workspace 2 %q", ws2)
	bootstrapCRD(t, ws2, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	sheriffsObjectCountName := corev1.ResourceName("count/sheriffs." + group)

	// Test with 2 workspaces to make sure quota is independent per workspace
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
		framework.Eventually(t, func() (bool, string) {
			quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(ctx, quotaName, metav1.GetOptions{})
			require.NoError(t, err, "error getting ws %d quota %s|default/quota: %v", wsIndex, ws, err)

			used, ok := quota.Status.Used[sheriffsObjectCountName]
			return ok && used.Equal(resource.MustParse("0")), fmt.Sprintf("ok=%t, used=%s", ok, used.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for ws %d quota to show usage in status", wsIndex)

		t.Logf("Create 2 sheriffs to reach the quota limit")
		apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-1", wsIndex))
		apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-2", wsIndex))

		t.Logf("Make sure quota is enforcing limits")
		i := 0
		sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
		framework.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a sheriff")
			sheriff := NewSheriff(group, fmt.Sprintf("ws%d-%d", wsIndex, i))
			i++
			_, err := dynamicClusterClient.Cluster(ws).Resource(sheriffsGVR).Namespace("default").Create(ctx, sheriff, metav1.CreateOptions{})
			return apierrors.IsForbidden(err), fmt.Sprintf("%v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected sheriff creation")

	}
}

func TestClusterScopedQuota(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	orgClusterName := framework.NewOrganizationFixture(t, server)

	// Create more than 1 workspace with the same quota restrictions to validate that after we create the first workspace
	// and fill its quota to capacity, subsequent workspaces have independent quota.
	for i := 0; i < 3; i++ {
		ws := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("quota-%d", i))

		const adminNamespace = "admin"
		t.Logf("Creating %q namespace %q", ws, adminNamespace)
		framework.Eventually(t, func() (success bool, reason string) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: adminNamespace,
				},
			}

			_, err := kubeClusterClient.Cluster(ws).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			return err == nil || apierrors.IsAlreadyExists(err), fmt.Sprintf("%v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating %q namespace", adminNamespace)

		t.Logf("Creating a child workspace in %q to make sure the quota controller counts it", ws)
		_ = framework.NewWorkspaceFixture(t, server, ws, framework.WithName("child"))

		const quotaName = "cluster-scoped"
		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name: quotaName,
				Annotations: map[string]string{
					"experimental.quota.kcp.dev/cluster-scoped": "true",
				},
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					"count/configmaps":                 resource.MustParse("3"),
					"count/workspaces.tenancy.kcp.dev": resource.MustParse("2"),
				},
			},
		}

		t.Logf("Creating cluster-scoped quota in %q", ws)
		quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas(adminNamespace).Create(ctx, quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating quota in %q", ws)

		t.Logf("Waiting for %q quota to show usage", ws)
		framework.Eventually(t, func() (bool, string) {
			quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas(adminNamespace).Get(ctx, quotaName, metav1.GetOptions{})
			require.NoError(t, err, "Error getting %q quota: %v", ws, err)

			used, ok := quota.Status.Used["count/configmaps"]
			if !ok {
				return false, fmt.Sprintf("waiting for %q count/configmaps to show up in used", ws)
			}
			// 1 for each kube-root-ca.crt x 2 namespaces = 2
			if !used.Equal(resource.MustParse("2")) {
				return false, fmt.Sprintf("waiting for %q count/configmaps %s to be 2", ws, used.String())
			}

			used, ok = quota.Status.Used["count/workspaces.tenancy.kcp.dev"]
			if !ok {
				return false, fmt.Sprintf("waiting for %q count/workspaces.tenancy.kcp.dev to show up in used", ws)
			}
			if !used.Equal(resource.MustParse("1")) {
				return false, fmt.Sprintf("waiting for %q count/workspaces.tenancy.kcp.dev %s to be 1", ws, used.String())
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 2 used configmaps and 1 used workspace")

		t.Logf("Make sure quota is enforcing configmap limits for %q", ws)
		framework.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a configmap in %q", ws)
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
			_, err = kubeClusterClient.Cluster(ws).CoreV1().ConfigMaps("default").Create(ctx, cm, metav1.CreateOptions{})
			return apierrors.IsForbidden(err), fmt.Sprintf("%v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected configmap creation in %q", ws)

		t.Logf("Make sure quota is enforcing workspace limits for %q", ws)
		framework.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a workspace in %q", ws)
			childWS := &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "child-",
				},
			}
			_, err = kcpClusterClient.TenancyV1beta1().Workspaces().Cluster(ws).Create(ctx, childWS, metav1.CreateOptions{})
			return apierrors.IsForbidden(err), fmt.Sprintf("%v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected workspace creation in %q", ws)
	}
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Name,
	client kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	t.Cleanup(cancelFunc)

	err := configcrds.CreateSingle(ctx, client.Cluster(clusterName), crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}

// NewSheriff returns a new *unstructured.Unstructured for a Sheriff with the given group and name.
func NewSheriff(group, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": group + "/v1",
			"kind":       "Sheriff",
			"metadata": map[string]interface{}{
				"name": name,
			},
		},
	}
}
