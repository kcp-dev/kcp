/*
Copyright 2023 The KCP Authors.

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

package apibinding

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPermissionClaimsByName(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName, _ := framework.NewOrganizationFixture(t, server)
	_, serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("provider"))
	_, consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer"))

	// Use the cluster hash since we're not using the front proxy here
	serviceProviderPath := logicalcluster.NewPath(serviceProviderWorkspace.Spec.Cluster)
	consumerPath := logicalcluster.NewPath(consumerWorkspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	claimedNamespace := "claimed-ns"
	unclaimedNamespace := "unclaimed-ns"

	var sheriffExport *apisv1alpha1.APIExport
	{
		t.Logf("==== setup ====")
		t.Logf("installing a sheriff APIResourceSchema and APIExport into workspace %q", serviceProviderPath)
		apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderPath, kcpClusterClient, "wild.wild.west", "board the wanderer")

		t.Logf("waiting for sheriff APIExport to be ready")
		framework.Eventually(t, func() (done bool, str string) {
			sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}

			if conditions.IsTrue(sheriffExport, apisv1alpha1.APIExportIdentityValid) {
				return true, ""
			}
			return false, "not done waiting for APIExportIdentity to be marked valid, no condition exists"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be valid with identity hash")
		require.NotNil(t, sheriffExport)

		t.Logf("creating claimed consumer namespace")
		_, err := kubeClusterClient.Cluster(consumerPath).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: claimedNamespace,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create ns-1")

		t.Logf("waiting for namespace to exist")
		framework.Eventually(t, func() (done bool, str string) {
			ns, err := kubeClusterClient.Cluster(consumerPath).CoreV1().Namespaces().Get(ctx, claimedNamespace, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}

			if ns.Status.Phase == v1.NamespaceActive {
				return true, ""
			}

			return false, "not done waiting for ns1 to be created"
		}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for namespace to be ready")
		t.Logf("namespace %s ready", claimedNamespace)

		t.Logf("creating unclaimed consumer namespace")
		_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: unclaimedNamespace,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create %s", unclaimedNamespace)

		t.Logf("waiting for namespace %s to exist", unclaimedNamespace)
		framework.Eventually(t, func() (done bool, str string) {
			ns, err := kubeClusterClient.Cluster(consumerPath).CoreV1().Namespaces().Get(ctx, unclaimedNamespace, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}

			if ns.Status.Phase == v1.NamespaceActive {
				return true, ""
			}

			return false, "not done waiting for namespace to be created"
		}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for namespace to be ready")
		t.Logf("namespace %s ready", unclaimedNamespace)

		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("setting PermissionClaims on APIExport %s to select all names in one namespace", sheriffExport.Name)
		sheriffExport.Spec.PermissionClaims = makeNarrowCMPermissionClaims(apisv1alpha1.ResourceSelectorAll, claimedNamespace)
		framework.Eventually(t, func() (done bool, str string) {
			updatedSheriffExport, err := kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Update(ctx, sheriffExport, metav1.UpdateOptions{})
			if err != nil {
				return false, err.Error()
			}

			sheriffExport = updatedSheriffExport
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be updated with PermissionClaims")

		t.Logf("binding consumer cluster and namespace to provider export")
		binding := bindConsumerToProviderCMExport(ctx, t, consumerPath, serviceProviderPath, kcpClusterClient, claimedNamespace, apisv1alpha1.ResourceSelectorAll)
		framework.EventuallyReady(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		})
		require.NotNil(t, binding)
	}

	t.Logf("creating shared APIExport VirtualWorkspace client for export %s", sheriffExport.Name)
	apiExportVWCfg := rest.CopyConfig(cfg)
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	framework.Eventually(t, func() (bool, string) {
		apiExport, err := kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, sheriffExport.Name, metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExport))
		require.NoError(t, err)
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExport.Status.VirtualWorkspaces)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	t.Logf("vwHost: %s", apiExportVWCfg.Host)
	apiExportClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Logf("==== create and update in claimed namespace ====")
	{
		t.Logf("verify we can create a new configmap in consumer namespace via the sheriff export VirtualWorkspace URL")
		cm := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "confmap1",
			},
		}
		framework.Eventually(t, func() (done bool, str string) {
			cm, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}

			require.Equal(t, claimedNamespace, cm.Namespace)
			require.Equal(t, cm.Name, "confmap1")
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to create configmap in consumer namespace")

		t.Logf("verify we can update a configmap in consumer workspace via the APIExport VirtualWorkspace URL")
		cm.Data = map[string]string{
			"something": "new",
		}
		framework.Eventually(t, func() (done bool, str string) {
			updatedCM, err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Update(ctx, cm, metav1.UpdateOptions{})
			if err != nil {
				return false, err.Error()
			}
			require.NotNil(t, updatedCM.Data)
			require.Equal(t, "new", updatedCM.Data["something"])
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to update configmap in consumer namespace %s, %v", claimedNamespace, cm)
	}

	t.Logf("==== creation in unclaimed namespace ====")
	{
		t.Logf("ensure that configmaps in an unspecified namespace cannot be created via APIExport VirtualWorkspace URL")
		framework.Eventually(t, func() (done bool, str string) {
			_, err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Create(ctx, &v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: "confmap2",
				},
			}, metav1.CreateOptions{})

			if err != nil && !apierrors.IsForbidden(err) {
				return false, err.Error()
			}

			if err == nil {
				return false, "unexpected successful create"
			}

			return true, "creation forbidden"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to create configmap in consumer namespace")
		t.Logf("creation of configmap in namespace %s successfully forbidden", unclaimedNamespace)
	}

	{
		t.Logf("==== create and update in any namespace ====")
		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("setting PermissionClaims on APIExport %s to allow configmap by explicit name, in any namespace", sheriffExport.Name)
		sheriffExport.Spec.PermissionClaims = makeNarrowCMPermissionClaims("confmap1", apisv1alpha1.ResourceSelectorAll)
		framework.Eventually(t, func() (done bool, str string) {
			sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Update(ctx, sheriffExport, metav1.UpdateOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be updated with PermissionClaims")

		t.Logf("updating consumer API Bindings with new permissionclaim")
		binding := bindConsumerToProviderCMExport(ctx, t, consumerPath, serviceProviderPath, kcpClusterClient, apisv1alpha1.ResourceSelectorAll, "confmap1")
		require.NotNil(t, binding)

		cm := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "confmap1",
			},
		}
		t.Logf("creating configmap %s in NS %s", cm.Name, unclaimedNamespace)
		framework.Eventually(t, func() (done bool, str string) {
			cm, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to create configmap in consumer namespace")
		require.Equal(t, unclaimedNamespace, cm.Namespace)
		require.NoError(t, err)
		t.Logf("cluster for configmap %s: %s", cm.Name, logicalcluster.From(cm).String())

		framework.Eventually(t, func() (done bool, str string) {
			updatedCM, err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Get(ctx, cm.Name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			require.NoError(t, err)
			require.Equal(t, unclaimedNamespace, updatedCM.Namespace)

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to get configmap in %s", unclaimedNamespace)

		t.Logf("verify we can update a configmap in consumer workspace via the APIExport VirtualWorkspace URL")
		framework.Eventually(t, func() (bool, string) {
			cm, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Get(ctx, cm.Name, metav1.GetOptions{})
			require.NoError(t, err)
			cm.Data = map[string]string{
				"something": "new",
			}
			newCM, err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Update(ctx, cm, metav1.UpdateOptions{})
			if apierrors.IsNotFound(err) {
				t.Logf("couldn't find configmap in %s", unclaimedNamespace)
			}
			if err != nil {
				return false, err.Error()
			}
			require.Equal(t, newCM.Data["something"], "new")
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to update configmap in %s", unclaimedNamespace)
	}

	t.Logf("==== create in unclaimed namespace forbidden ===")
	{
		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("setting PermissionClaims on APIExport %s to only claim one namespace", sheriffExport.Name)
		sheriffExport.Spec.PermissionClaims = makeNarrowCMPermissionClaims("", claimedNamespace)
		framework.Eventually(t, func() (done bool, str string) {
			sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Update(ctx, sheriffExport, metav1.UpdateOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be updated with PermissionClaims")

		t.Logf("updating consumer APIBindings with single namespace PermissionClaim")
		binding := bindConsumerToProviderCMExport(ctx, t, consumerPath, serviceProviderPath, kcpClusterClient, claimedNamespace, apisv1alpha1.ResourceSelectorAll)
		require.NotNil(t, binding)
		cm := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "confmap3",
			},
		}
		framework.Eventually(t, func() (done bool, str string) {
			cm, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(unclaimedNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if apierrors.IsForbidden(err) {
				return true, ""
			}
			if err != nil {
				return false, err.Error()
			}

			return false, "unexpected create"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "never received forbidden error")
	}

	t.Logf("==== create/update with single claimed name and single claimed namespace ====")
	{
		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("setting PermissionClaims on APIExport %s to only allow a specific object name in a specific namespace", sheriffExport.Name)
		sheriffExport.Spec.PermissionClaims = makeNarrowCMPermissionClaims("unique", claimedNamespace)
		framework.Eventually(t, func() (done bool, str string) {
			updatedSheriffExport, err := kcpClusterClient.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Update(ctx, sheriffExport, metav1.UpdateOptions{})
			if err != nil {
				return false, err.Error()
			}

			sheriffExport = updatedSheriffExport
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be updated with PermissionClaims")

		t.Logf("updating consumer APIBindings with single name/namespace PermissionClaim")
		binding := bindConsumerToProviderCMExport(ctx, t, consumerPath, serviceProviderPath, kcpClusterClient, claimedNamespace, "unique")
		require.NotNil(t, binding)
		cm := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "unique",
			},
		}
		t.Logf("confirm configmap claimed by name can be created")
		framework.Eventually(t, func() (done bool, str string) {
			cm, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out trying to create configmap in %s", claimedNamespace)
		require.Equal(t, claimedNamespace, cm.Namespace)
		require.NoError(t, err)

		t.Logf("confirm configmaps with unpermitted names cannot be created")
		badCM := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "not-unique",
			},
		}
		framework.Eventually(t, func() (done bool, str string) {
			_, err = apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Create(ctx, badCM, metav1.CreateOptions{})
			if apierrors.IsForbidden(err) {
				return true, ""
			}
			if err != nil {
				return false, err.Error()
			}

			return false, "unexpected create"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "never received forbidden error")

		t.Logf("create a configmap that does not match permision claims in consumer namespace, outside the APIExport VirtualWorkspace")
		framework.Eventually(t, func() (done bool, str string) {
			_, err := kubeClusterClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Create(ctx, badCM, metav1.CreateOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, "created configmap outside APIExport VirtualWorkspace URL"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not create configmap outside APIExport VirtualWorkspace URL")
	}

	t.Logf("==== list and delete through APIExport VirtualWorkspace URL ====")
	{
		t.Logf("listing configmaps through APIExport VirtualWorkspace URL only returns applicable objects")
		framework.Eventually(t, func() (done bool, str string) {
			list, err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).List(ctx, metav1.ListOptions{})
			require.Equal(t, 1, len(list.Items))
			require.Equal(t, "unique", list.Items[0].Name)
			if err != nil {
				return false, err.Error()
			}

			return true, "got expected items in list"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not list configmaps")

		t.Logf("deleting configmaps through the APIExport VirtualWorkspace URL only deletes the claimed ones")
		framework.Eventually(t, func() (done bool, str string) {
			err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}

			return true, "successfully deleted configmaps through APIExport VirtualWorkspace URL"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "timed out waiting to delete configmaps")

		t.Logf("unclaimed configmaps still exist via workspace URL")
		framework.Eventually(t, func() (done bool, str string) {
			list, err := kubeClusterClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}

			require.Equal(t, 3, len(list.Items))
			names := make([]string, 0, 3)
			for _, i := range list.Items {
				names = append(names, i.Name)
			}
			require.ElementsMatch(t, names, []string{"not-unique", "kube-root-ca.crt", "confmap1"})

			return true, "got expected items in list"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not list configmaps")

		t.Logf("trying to delete single unclaimed configmap through APIExport VirtualWorkspace URL should fail")
		framework.Eventually(t, func() (done bool, str string) {
			err := apiExportClient.Cluster(consumerPath).CoreV1().ConfigMaps(claimedNamespace).Delete(ctx, "confmap1", metav1.DeleteOptions{})
			if apierrors.IsForbidden(err) {
				return true, ""
			}

			if err != nil {
				return false, err.Error()
			}

			return false, "delete unexpectedly successful"
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not delete configmap")
	}
}

// makeNarrowCMPermissionClaim creates a PermissionClaim for ConfigMaps scoped to just a name, just a namespace, or both.
func makeNarrowCMPermissionClaims(name, namespace string) []apisv1alpha1.PermissionClaim {
	return []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
			All:           false,
			ResourceSelector: []apisv1alpha1.ResourceSelector{
				{
					Names:      []string{name},
					Namespaces: []string{namespace},
				},
			},
		},
	}
}

func makeAcceptedCMPermissionClaims(namespace, name string) []apisv1alpha1.AcceptablePermissionClaim {
	return []apisv1alpha1.AcceptablePermissionClaim{
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
				ResourceSelector: []apisv1alpha1.ResourceSelector{
					{
						Names:      []string{name},
						Namespaces: []string{namespace},
					},
				},
				All: false,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
	}
}
func bindConsumerToProviderCMExport(
	ctx context.Context,
	t *testing.T,
	consumerPath logicalcluster.Path,
	providerClusterPath logicalcluster.Path,
	kcpClusterClients kcpclientset.ClusterInterface,
	cmNamespace, cmName string,
) *apisv1alpha1.APIBinding {
	t.Helper()
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-and-configmaps",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerClusterPath.String(),
					Name: "wild.wild.west",
				},
			},
			PermissionClaims: makeAcceptedCMPermissionClaims(cmNamespace, cmName),
		},
	}

	binding := &apisv1alpha1.APIBinding{}
	framework.Eventually(t, func() (bool, string) {
		var err error
		t.Logf("create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerPath, providerClusterPath)
		binding, err = kcpClusterClients.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err == nil {
			t.Logf("created APIBinding %s", apiBinding.Name)
			return true, ""
		}
		if !apierrors.IsAlreadyExists(err) {
			return false, err.Error()
		}
		binding, err = kcpClusterClients.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		require.NoError(t, err)
		t.Logf("APIbinding %s already exists, updating PermissionClaims", apiBinding.Name)
		binding.Spec.PermissionClaims = makeAcceptedCMPermissionClaims(cmNamespace, cmName)
		binding, err = kcpClusterClients.Cluster(consumerPath).ApisV1alpha1().APIBindings().Update(ctx, binding, metav1.UpdateOptions{})
		if err == nil {
			t.Logf("updated APIBinding %s", apiBinding.Name)
			return true, "updated APIBinding"
		}
		return false, err.Error()
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClients.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.InitialBindingCompleted), "timed out waiting for APIBinding to be ready")

	return binding
}
