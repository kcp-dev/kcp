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

package quota

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestKubeQuotaBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create more than 1 workspace with the same quota restrictions to validate that after we create the first workspace
	// and fill its quota to capacity, subsequent workspaces have independent quota.
	for i := range 3 {
		wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("quota-%d", i))

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
		ws1Quota, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Create(t.Context(), ws1Quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating ws quota")

		t.Logf("Waiting for ws quota to show used configmaps (kube-root-ca.crt)")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			ws1Quota, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
			require.NoError(t, err, "Error getting ws quota %s|default/quota: %v", wsPath, err)

			used, ok := ws1Quota.Status.Used["count/configmaps"]
			return ok && used.Equal(resource.MustParse("1")), fmt.Sprintf("ok=%t, used=%s", ok, used.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 1 used configmaps")

		t.Logf("Make sure quota is enforcing limits")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a configmap")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
			if _, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{}); err != nil {
				return apierrors.IsForbidden(err), err.Error()
			}
			return false, "expected an error trying to create a configmap"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected configmap creation")
	}
}

// TestKubeQuotaCrossWorkspaceIsolation tests that identical quotas in identical namespaces in separate workspaces track resources separately.
func TestKubeQuotaCrossWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	wsAPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("a"))
	wsBPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("b"))

	makeQuota := func() *corev1.ResourceQuota {
		return &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "quota"},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					"count/configmaps": resource.MustParse("10"),
				},
			},
		}
	}

	t.Logf("Creating identically-named quotas in default ns of both workspaces")
	_, err = kubeClusterClient.Cluster(wsAPath).CoreV1().ResourceQuotas("default").Create(t.Context(), makeQuota(), metav1.CreateOptions{})
	require.NoError(t, err, "creating quota in workspace A")
	_, err = kubeClusterClient.Cluster(wsBPath).CoreV1().ResourceQuotas("default").Create(t.Context(), makeQuota(), metav1.CreateOptions{})
	require.NoError(t, err, "creating quota in workspace B")

	t.Logf("Waiting for both quotas to report the initial 1 used configmap")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qa, err := kubeClusterClient.Cluster(wsAPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		qb, err := kubeClusterClient.Cluster(wsBPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		ua, oka := qa.Status.Used["count/configmaps"]
		ub, okb := qb.Status.Used["count/configmaps"]
		return oka && okb && ua.Equal(resource.MustParse("1")) && ub.Equal(resource.MustParse("1")),
			fmt.Sprintf("a: ok=%t used=%s; b: ok=%t used=%s", oka, ua.String(), okb, ub.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "initial usage never converged on both workspaces")

	t.Logf("Creating 3 configmaps in workspace A")
	for i := range 3 {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cm-a-%d", i)}}
		_, err := kubeClusterClient.Cluster(wsAPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		require.NoError(t, err, "creating configmap %d in A", i)
	}

	t.Logf("Waiting for workspace A quota to reflect the new configmaps and asserting workspace B remains at 1")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qa, err := kubeClusterClient.Cluster(wsAPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		qb, err := kubeClusterClient.Cluster(wsBPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)

		ua := qa.Status.Used["count/configmaps"]
		ub := qb.Status.Used["count/configmaps"]

		// Hard guard: workspace B usage must never reflect workspace A's writes.
		require.True(t, ub.Equal(resource.MustParse("1")),
			"workspace B quota leaked from workspace A writes; B used=%s", ub.String())

		return ua.Equal(resource.MustParse("4")),
			fmt.Sprintf("A used=%s (want 4), B used=%s (want 1)", ua.String(), ub.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace A usage never reached 4 while B held at 1")

	t.Logf("Lowering workspace A quota Hard to 4 (== current Used) to put it exactly at the limit")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qa, err := kubeClusterClient.Cluster(wsAPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		qa.Spec.Hard["count/configmaps"] = resource.MustParse("4")
		_, err = kubeClusterClient.Cluster(wsAPath).CoreV1().ResourceQuotas("default").Update(t.Context(), qa, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, err.Error()
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not patch workspace A quota Hard to 4")

	t.Logf("Confirming workspace A is now at its hard limit and rejects creates")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "blocked-"}}
		_, err := kubeClusterClient.Cluster(wsAPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		if err != nil {
			return apierrors.IsForbidden(err), err.Error()
		}
		return false, "expected forbidden creating configmap at limit"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace A never enforced the lowered limit")

	t.Logf("Deleting one configmap in workspace A to free headroom")
	err = kubeClusterClient.Cluster(wsAPath).CoreV1().ConfigMaps("default").Delete(t.Context(), "cm-a-0", metav1.DeleteOptions{})
	require.NoError(t, err, "deleting cm-a-0 in workspace A")

	t.Logf("Asserting workspace B remains at 1 and a create in workspace A now succeeds")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qb, err := kubeClusterClient.Cluster(wsBPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		ub := qb.Status.Used["count/configmaps"]
		require.True(t, ub.Equal(resource.MustParse("1")), "workspace B leaked from A delete; B used=%s", ub.String())

		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-a-after-delete"}}
		_, err = kubeClusterClient.Cluster(wsAPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, ""
			}
			return false, fmt.Sprintf("create after delete still rejected: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace A never accepted a create after freeing headroom")
}

// TestKubeQuotaNamespaceIsolation tests that identical quotas in the same workspace in separate namespace track resources separately.
func TestKubeQuotaNamespaceIsolation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ns-iso"))

	const nsA = "ns-a"
	const nsB = "ns-b"

	t.Logf("Creating two namespaces in %q", wsPath)
	for _, ns := range []string{nsA, nsB} {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			if err != nil {
				return apierrors.IsAlreadyExists(err), err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "creating namespace %s", ns)
	}

	makeQuota := func() *corev1.ResourceQuota {
		return &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "quota"},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					"count/configmaps": resource.MustParse("10"),
				},
			},
		}
	}

	t.Logf("Creating identically-named quotas in both namespaces")
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsA).Create(t.Context(), makeQuota(), metav1.CreateOptions{})
	require.NoError(t, err, "creating quota in nsA")
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsB).Create(t.Context(), makeQuota(), metav1.CreateOptions{})
	require.NoError(t, err, "creating quota in nsB")

	t.Logf("Waiting for both quotas to report the initial 1 used configmap (kube-root-ca.crt per namespace)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qa, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsA).Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		qb, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsB).Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		ua, oka := qa.Status.Used["count/configmaps"]
		ub, okb := qb.Status.Used["count/configmaps"]
		return oka && okb && ua.Equal(resource.MustParse("1")) && ub.Equal(resource.MustParse("1")),
			fmt.Sprintf("a: ok=%t used=%s; b: ok=%t used=%s", oka, ua.String(), okb, ub.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "initial per-namespace usage never converged")

	t.Logf("Creating 3 configmaps in nsA")
	for i := range 3 {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cm-a-%d", i)}}
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps(nsA).Create(t.Context(), cm, metav1.CreateOptions{})
		require.NoError(t, err, "creating configmap %d in nsA", i)
	}

	t.Logf("Asserting nsA quota reflects the new configmaps and nsB remains at 1")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		qa, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsA).Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		qb, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(nsB).Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)

		ua := qa.Status.Used["count/configmaps"]
		ub := qb.Status.Used["count/configmaps"]

		require.True(t, ub.Equal(resource.MustParse("1")),
			"nsB quota leaked from nsA writes; B used=%s", ub.String())

		return ua.Equal(resource.MustParse("4")),
			fmt.Sprintf("A used=%s (want 4), B used=%s (want 1)", ua.String(), ub.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "nsA usage never reached 4 while nsB held at 1")
}

func TestKubeQuotaCoreV1TypesFromBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := kcptesting.SharedKcpServer(t)

	// Test multiple workspaces in parallel
	for i := range 5 {
		t.Run(fmt.Sprintf("tc%d", i), func(t *testing.T) {
			t.Parallel()

			orgPath, _ := kcptesting.NewWorkspaceFixture(t, source, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
			apiProviderPath, _ := kcptesting.NewWorkspaceFixture(t, source, orgPath, kcptesting.WithName("api-provider"))
			userPath, _ := kcptesting.NewWorkspaceFixture(t, source, orgPath, kcptesting.WithName("user"))

			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
			require.NoError(t, err)
			kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
			require.NoError(t, err)

			t.Logf("Check that there is no services resource in the user workspace")
			_, err = kubeClusterClient.Cluster(userPath).CoreV1().Services("").List(t.Context(), metav1.ListOptions{})
			require.Error(t, err)

			t.Logf("Getting services CRD")
			servicesCRD := kubefixtures.CRD(t, metav1.GroupResource{Group: "core.k8s.io", Resource: "services"})

			t.Logf("Converting services CRD to APIResourceSchema")
			servicesAPIResourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(servicesCRD, "some-prefix")
			require.NoError(t, err, "error converting CRD to APIResourceSchema")

			t.Logf("Creating APIResourceSchema")
			_, err = kcpClusterClient.Cluster(apiProviderPath).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), servicesAPIResourceSchema, metav1.CreateOptions{})
			require.NoError(t, err, "error creating APIResourceSchema")

			t.Logf("Creating APIExport")
			servicesAPIExport := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "services",
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Name:   "services",
							Group:  "",
							Schema: servicesAPIResourceSchema.Name,
							Storage: apisv1alpha2.ResourceSchemaStorage{
								CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
							},
						},
					},
				},
			}

			_, err = kcpClusterClient.Cluster(apiProviderPath).ApisV1alpha2().APIExports().Create(t.Context(), servicesAPIExport, metav1.CreateOptions{})
			require.NoError(t, err, "error creating APIExport")

			t.Logf("Create a binding in the user workspace")
			binding := &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "services",
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: apiProviderPath.String(),
							Name: servicesAPIExport.Name,
						},
					},
				},
			}

			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err := kcpClusterClient.Cluster(userPath).ApisV1alpha2().APIBindings().Create(t.Context(), binding, metav1.CreateOptions{})
				return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating APIBinding")

			t.Logf("Wait for binding to be ready")
			kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
				return kcpClusterClient.Cluster(userPath).ApisV1alpha2().APIBindings().Get(t.Context(), binding.Name, metav1.GetOptions{})
			}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

			t.Logf("Wait for being able to list Services in the user workspace")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err := kubeClusterClient.Cluster(userPath).CoreV1().Services("").List(t.Context(), metav1.ListOptions{})
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

			_, err = kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Create(t.Context(), quota, metav1.CreateOptions{})
			require.NoError(t, err, "error creating quota")

			t.Logf("Waiting for quota to show 0 used Services")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				quota, err = kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
				require.NoError(t, err, "Error getting ws quota %s|default/quota: %v", userPath, err)

				used, ok := quota.Status.Used["count/services"]
				return ok && used.Equal(resource.MustParse("0")), used.String()
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 0 used Services")

			t.Logf("Make sure quota is enforcing limits")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				t.Logf("Trying to create a service")
				service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
				_, err = kubeClusterClient.Cluster(userPath).CoreV1().Services("default").Create(t.Context(), service, metav1.CreateOptions{})
				if err != nil {
					return apierrors.IsForbidden(err), err.Error()
				}
				return false, "expected an error trying to create a service"
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected service creation")

			t.Logf("Adding count/configmaps to the same quota so we can verify the controller still drives unrelated dimensions after the binding goes away")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				q, err := kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
				require.NoError(t, err)
				if q.Spec.Hard == nil {
					q.Spec.Hard = map[corev1.ResourceName]resource.Quantity{}
				}
				q.Spec.Hard["count/configmaps"] = resource.MustParse("100")
				_, err = kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Update(t.Context(), q, metav1.UpdateOptions{})
				if apierrors.IsConflict(err) {
					return false, err.Error()
				}
				require.NoError(t, err)
				return true, ""
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not add count/configmaps to quota Hard")

			t.Logf("Deleting the APIBinding to remove the services GVR from this workspace")
			err = kcpClusterClient.Cluster(userPath).ApisV1alpha2().APIBindings().Delete(t.Context(), binding.Name, metav1.DeleteOptions{})
			require.NoError(t, err, "deleting APIBinding")

			t.Logf("Waiting for services to disappear from the workspace")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err := kubeClusterClient.Cluster(userPath).CoreV1().Services("").List(t.Context(), metav1.ListOptions{})
				return err != nil, fmt.Sprintf("services still listable: err=%v", err)
			}, wait.ForeverTestTimeout, 200*time.Millisecond, "services never became unavailable")

			t.Logf("Asserting the quota controller still updates an unrelated dimension after the GVR removal")
			baselineCM, err := kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
			require.NoError(t, err)
			baselineUsed := baselineCM.Status.Used["count/configmaps"]

			_, err = kubeClusterClient.Cluster(userPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-after-unbind"}}, metav1.CreateOptions{})
			require.NoError(t, err, "creating configmap after unbind")

			want := baselineUsed.DeepCopy()
			want.Add(resource.MustParse("1"))
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				q, err := kubeClusterClient.Cluster(userPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
				require.NoError(t, err)
				used := q.Status.Used["count/configmaps"]
				return used.Equal(want), fmt.Sprintf("used=%s want=%s (baseline=%s)", used.String(), want.String(), baselineUsed.String())
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota controller stalled on count/configmaps after APIBinding deletion")
		})
	}
}

func TestKubeQuotaNormalCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	group := framework.UniqueGroup(".io")

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	ws1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("one"))
	ws2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("two"))

	t.Logf("Install a normal sheriffs CRD into workspace 1 %q", ws1Path)
	bootstrapCRD(t, ws1Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD with a different schema into workspace 2 %q", ws2Path)
	bootstrapCRD(t, ws2Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	sheriffsObjectCountName := corev1.ResourceName("count/sheriffs." + group)

	// Test with 2 workspaces to make sure quota is independent per workspace
	workspaces := []logicalcluster.Path{ws1Path, ws2Path}
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
		quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Create(t.Context(), quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating ws %d quota", wsIndex)

		t.Logf("Waiting for ws %d quota to show usage", wsIndex)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			quota, err = kubeClusterClient.Cluster(ws).CoreV1().ResourceQuotas("default").Get(t.Context(), quotaName, metav1.GetOptions{})
			require.NoError(t, err, "error getting ws %d quota %s|default/quota: %v", wsIndex, ws, err)

			used, ok := quota.Status.Used[sheriffsObjectCountName]
			return ok && used.Equal(resource.MustParse("0")), fmt.Sprintf("ok=%t, used=%s", ok, used.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for ws %d quota to show usage in status", wsIndex)

		t.Logf("Create 2 sheriffs to reach the quota limit")
		createSheriff(t.Context(), t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-1", wsIndex))
		createSheriff(t.Context(), t, dynamicClusterClient, ws, group, fmt.Sprintf("ws%d-2", wsIndex))

		t.Logf("Make sure quota is enforcing limits")
		i := 0
		sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a sheriff")
			sheriff := newSheriff(group, fmt.Sprintf("ws%d-%d", wsIndex, i))
			i++
			_, err := dynamicClusterClient.Cluster(ws).Resource(sheriffsGVR).Namespace("default").Create(t.Context(), sheriff, metav1.CreateOptions{})
			return apierrors.IsForbidden(err), fmt.Sprintf("expected a forbidden error, got: %v", err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected sheriff creation")
	}
}

func TestClusterScopedQuota(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Create more than 1 workspace with the same quota restrictions to validate that after we create the first workspace
	// and fill its quota to capacity, subsequent workspaces have independent quota.
	for i := range 3 {
		wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("quota-%d", i))

		const adminNamespace = "admin"
		t.Logf("Creating %q namespace %q", wsPath, adminNamespace)
		kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: adminNamespace,
				},
			}

			_, err := kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces().Create(t.Context(), ns, metav1.CreateOptions{})
			if err != nil {
				return apierrors.IsAlreadyExists(err), err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating %q namespace", adminNamespace)

		t.Logf("Creating a child workspace in %q to make sure the quota controller counts it", wsPath)
		_, _ = kcptesting.NewWorkspaceFixture(t, server, wsPath, kcptesting.WithName("child"))

		const quotaName = "cluster-scoped"
		quota := &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{
				Name: quotaName,
				Annotations: map[string]string{
					"experimental.quota.kcp.io/cluster-scoped": "true",
				},
			},
			Spec: corev1.ResourceQuotaSpec{
				Hard: map[corev1.ResourceName]resource.Quantity{
					"count/configmaps":                resource.MustParse("3"),
					"count/workspaces.tenancy.kcp.io": resource.MustParse("2"),
				},
			},
		}

		t.Logf("Creating cluster-scoped quota in %q", wsPath)
		quota, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Create(t.Context(), quota, metav1.CreateOptions{})
		require.NoError(t, err, "error creating quota in %q", wsPath)

		t.Logf("Waiting for %q quota to show usage", wsPath)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			quota, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Get(t.Context(), quotaName, metav1.GetOptions{})
			require.NoError(t, err, "Error getting %q quota: %v", wsPath, err)

			used, ok := quota.Status.Used["count/configmaps"]
			if !ok {
				return false, fmt.Sprintf("waiting for %q count/configmaps to show up in used", wsPath)
			}
			// 1 for each kube-root-ca.crt x 3 namespaces = 3
			if !used.Equal(resource.MustParse("3")) {
				return false, fmt.Sprintf("waiting for %q count/configmaps %s to be 3", wsPath, used.String())
			}

			used, ok = quota.Status.Used["count/workspaces.tenancy.kcp.io"]
			if !ok {
				return false, fmt.Sprintf("waiting for %q count/workspaces.tenancy.kcp.io to show up in used", wsPath)
			}
			if !used.Equal(resource.MustParse("1")) {
				return false, fmt.Sprintf("waiting for %q count/workspaces.tenancy.kcp.io %s to be 1", wsPath, used.String())
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for 3 used configmaps and 1 used workspace")

		t.Logf("Make sure quota is enforcing configmap limits for %q", wsPath)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a configmap in %q", wsPath)
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "quota-"}}
			_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
			if err != nil {
				return apierrors.IsForbidden(err), err.Error()
			}
			return false, "expected an error trying to create a configmap"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected configmap creation in %q", wsPath)

		t.Logf("Make sure quota is enforcing workspace limits for %q", wsPath)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			t.Logf("Trying to create a workspace in %q", wsPath)
			childWS := &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "child-",
				},
			}
			_, err = kcpClusterClient.TenancyV1alpha1().Workspaces().Cluster(wsPath).Create(t.Context(), childWS, metav1.CreateOptions{})
			if err != nil {
				return apierrors.IsForbidden(err), err.Error()
			}
			return false, "expected an error trying to create a workspace"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never rejected workspace creation in %q", wsPath)

		t.Logf("Reading current cluster-scoped Used count for configmaps")
		var before resource.Quantity
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			q, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Get(t.Context(), quotaName, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			used, ok := q.Status.Used["count/configmaps"]
			if !ok {
				return false, "no count/configmaps in Used yet"
			}
			before = used
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "no cluster-scoped Used reading")

		t.Logf("Raising configmaps Hard above current Used so the new namespace's auto-configmap can be admitted")
		newHard := before.DeepCopy()
		newHard.Add(resource.MustParse("5"))
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			q, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Get(t.Context(), quotaName, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			q.Spec.Hard["count/configmaps"] = newHard
			_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Update(t.Context(), q, metav1.UpdateOptions{})
			if err == nil {
				return true, ""
			}
			return false, err.Error()
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not raise cluster-scoped configmaps Hard")

		extraNamespace := "cross-ns"
		t.Logf("Creating fresh namespace %q in %q", extraNamespace, wsPath)
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: extraNamespace}}, metav1.CreateOptions{})
		require.NoError(t, err, "creating %s namespace", extraNamespace)

		t.Logf("Asserting cluster-scoped Used grew to count the auto-created kube-root-ca.crt configmap from a different namespace")
		want := before.DeepCopy()
		want.Add(resource.MustParse("1"))
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			q, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas(adminNamespace).Get(t.Context(), quotaName, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			used, ok := q.Status.Used["count/configmaps"]
			return ok && used.Equal(want), fmt.Sprintf("ok=%t used=%s want=%s (before=%s)", ok, used.String(), want.String(), before.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "cluster-scoped Used did not pick up configmap from a different namespace")
	}
}

// TestKubeQuotaCRDChurn checks that installing and restinstalling a CRD keeps being tracked correctly by the quota controller.
func TestKubeQuotaCRDChurn(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("churn"))

	group := framework.UniqueGroup(".io")
	sheriffsObjectCountName := corev1.ResourceName("count/sheriffs." + group)
	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	sheriffsCRDName := "sheriffs." + group

	t.Logf("Creating quota gating sheriffs in %q", wsPath)
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Create(t.Context(), &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "quota"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: map[corev1.ResourceName]resource.Quantity{
				sheriffsObjectCountName: resource.MustParse("10"),
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "creating quota")

	for cycle := range 2 {
		t.Logf("Cycle %d: installing sheriffs CRD", cycle)
		bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), apifixtures.NewSheriffsCRDWithSchemaDescription(group, fmt.Sprintf("cycle-%d", cycle)))

		t.Logf("Cycle %d: creating 2 sheriffs", cycle)
		createSheriff(t.Context(), t, dynamicClusterClient, wsPath, group, fmt.Sprintf("c%d-1", cycle))
		createSheriff(t.Context(), t, dynamicClusterClient, wsPath, group, fmt.Sprintf("c%d-2", cycle))

		t.Logf("Cycle %d: waiting for quota Used to reach 2", cycle)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			q, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
			require.NoError(t, err)
			used, ok := q.Status.Used[sheriffsObjectCountName]
			return ok && used.Equal(resource.MustParse("2")),
				fmt.Sprintf("ok=%t used=%s (want 2)", ok, used.String())
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "cycle %d quota never reached 2", cycle)

		t.Logf("Cycle %d: deleting sheriffs and CRD", cycle)
		for _, name := range []string{fmt.Sprintf("c%d-1", cycle), fmt.Sprintf("c%d-2", cycle)} {
			err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVR).Namespace("default").Delete(t.Context(), name, metav1.DeleteOptions{})
			require.NoError(t, err, "deleting sheriff %s", name)
		}
		err = crdClusterClient.Cluster(wsPath).ApiextensionsV1().CustomResourceDefinitions().Delete(t.Context(), sheriffsCRDName, metav1.DeleteOptions{})
		require.NoError(t, err, "deleting CRD")

		t.Logf("Cycle %d: waiting for CRD to disappear", cycle)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := crdClusterClient.Cluster(wsPath).ApiextensionsV1().CustomResourceDefinitions().Get(t.Context(), sheriffsCRDName, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprintf("get CRD err=%v", err)
		}, wait.ForeverTestTimeout, 200*time.Millisecond, "CRD never deleted")
	}
}

// TestKubeQuotaQuotaLifecycle exercises the lifecylce of multiple quotas.
func TestKubeQuotaQuotaLifecycle(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("lifecycle"))

	t.Logf("Creating a quota with Hard=2 on count/configmaps")
	q := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "quota"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: map[corev1.ResourceName]resource.Quantity{
				"count/configmaps": resource.MustParse("2"),
			},
		},
	}
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Create(t.Context(), q, metav1.CreateOptions{})
	require.NoError(t, err, "creating quota")

	t.Logf("Waiting for Used=1 (kube-root-ca.crt) and confirming a second create is forbidden")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		used, ok := got.Status.Used["count/configmaps"]
		return ok && used.Equal(resource.MustParse("1")), fmt.Sprintf("ok=%t used=%s", ok, used.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Used never reached 1")

	// Fill to the limit so the next create is forbidden.
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-1"}}, metav1.CreateOptions{})
	require.NoError(t, err, "creating cm-1")

	t.Logf("Waiting for the controller to observe Used=2 before probing for Forbidden creates")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		used, ok := got.Status.Used["count/configmaps"]
		return ok && used.Equal(resource.MustParse("2")), fmt.Sprintf("ok=%t used=%s want=2", ok, used.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Used never reached 2 after creating cm-1")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "forbidden-"}}
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		if err != nil {
			return apierrors.IsForbidden(err), err.Error()
		}
		return false, "expected forbidden at Hard=2"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never enforced Hard=2")

	t.Logf("Raising Hard to 5 and asserting a previously-forbidden create now succeeds")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		got.Spec.Hard["count/configmaps"] = resource.MustParse("5")
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Update(t.Context(), got, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, err.Error()
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not raise Hard to 5")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-after-raise"}}, metav1.CreateOptions{})
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, ""
			}
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "create still forbidden after raising Hard")

	t.Logf("Lowering Hard to 1 (below current Used) and asserting creates are forbidden again")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		got.Spec.Hard["count/configmaps"] = resource.MustParse("1")
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Update(t.Context(), got, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, err.Error()
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not lower Hard to 1")

	t.Logf("Waiting for the controller to publish Status.Hard[count/configmaps]=1 before probing for Forbidden creates")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		hard, ok := got.Status.Hard["count/configmaps"]
		return ok && hard.Equal(resource.MustParse("1")), fmt.Sprintf("ok=%t hard=%s want=1", ok, hard.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Status.Hard never reflected the lowered Hard=1")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "forbidden-low-"}}
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		if err != nil {
			return apierrors.IsForbidden(err), err.Error()
		}
		return false, "expected forbidden at Hard=1"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "quota never enforced lowered Hard=1")

	t.Logf("Raising original quota back to Hard=100, then adding a tighter sibling quota with Hard=3")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "quota", metav1.GetOptions{})
		require.NoError(t, err)
		got.Spec.Hard["count/configmaps"] = resource.MustParse("100")
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Update(t.Context(), got, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, err.Error()
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not raise original Hard to 100")

	tight := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "tight"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: map[corev1.ResourceName]resource.Quantity{
				"count/configmaps": resource.MustParse("3"),
			},
		},
	}
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Create(t.Context(), tight, metav1.CreateOptions{})
	require.NoError(t, err, "creating tight quota")

	t.Logf("Waiting for the tight quota to populate Used (it must observe existing configmaps)")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Get(t.Context(), "tight", metav1.GetOptions{})
		require.NoError(t, err)
		used, ok := got.Status.Used["count/configmaps"]
		return ok && used.Equal(resource.MustParse("3")), fmt.Sprintf("ok=%t used=%s want=3", ok, used.String())
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "tight quota Used never reached 3")

	t.Logf("Asserting the tighter (binding) quota now blocks creates even though the original quota has slack")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{GenerateName: "forbidden-tight-"}}
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
		if err != nil {
			return apierrors.IsForbidden(err), err.Error()
		}
		return false, "expected forbidden from tight quota"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "tight quota never enforced as binding limit")

	t.Logf("Deleting both quotas and asserting creates succeed")
	require.NoError(t, kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Delete(t.Context(), "tight", metav1.DeleteOptions{}), "delete tight")
	require.NoError(t, kubeClusterClient.Cluster(wsPath).CoreV1().ResourceQuotas("default").Delete(t.Context(), "quota", metav1.DeleteOptions{}), "delete original quota")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm-after-delete"}}, metav1.CreateOptions{})
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, ""
			}
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "create still forbidden after deleting all quotas")
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Path,
	client kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	t.Helper()

	err := configcrds.CreateSingle(t.Context(), client.Cluster(clusterName), crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}

// newSheriff returns a new *unstructured.Unstructured for a Sheriff with the given group and name.
func newSheriff(group, name string) *unstructured.Unstructured {
	sheriff := &unstructured.Unstructured{}
	sheriff.SetAPIVersion(group + "/v1")
	sheriff.SetKind("Sheriff")
	sheriff.SetName(name)

	return sheriff
}

func createSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Path,
	group, name string,
) {
	t.Helper()

	name = strings.ReplaceAll(name, ":", "-")

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	sheriff := newSheriff(group, name)

	// CRDs are asynchronously served because they are informer based.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		if _, err := dynamicClusterClient.Cluster(clusterName).Resource(sheriffsGVR).Namespace("default").Create(t.Context(), sheriff, metav1.CreateOptions{}); err != nil {
			return false, fmt.Sprintf("failed to create Sheriff %s|%s: %v", clusterName, name, err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "error creating Sheriff %s|%s", clusterName, name)
}
