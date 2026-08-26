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

package apiexport

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMintServiceAccountTokenThroughVW(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	const providerSAClaimLabel = "custom.provider/label"

	randomStringKey := "test"
	randomString := utilrand.String(8)
	t.Logf("Create a ConfigMap in the consumer with content %q=%q", randomStringKey, randomString)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cm",
			Namespace: "default",
		},
		Data: map[string]string{
			randomStringKey: randomString,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().ConfigMaps(cm.Namespace).Create(t.Context(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Setup an unclaimed ServiceAccount in consumer with cluster-admin")
	unclaimedSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unclaimed-sa",
			Namespace: "default",
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().ServiceAccounts("default").Create(t.Context(), unclaimedSA, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Setup an claimed ServiceAccount in consumer with cluster-admin")
	claimedSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claimed-sa",
			Namespace: "default",
			Labels: map[string]string{
				providerSAClaimLabel: "true",
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().ServiceAccounts("default").Create(t.Context(), claimedSA, metav1.CreateOptions{})
	require.NoError(t, err)

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: claimedSA.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      claimedSA.Name,
				Namespace: claimedSA.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "cluster-admin",
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create APIExport in provider with claims for ServiceAccount")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sa-token",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Resource: "serviceaccounts",
					},
					Verbs: []string{"get", "list"},
					DefaultSelector: &apisv1alpha2.PermissionClaimSelector{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								providerSAClaimLabel: "true",
							},
						},
					},
				},
				{
					GroupResource: apisv1alpha2.GroupResource{
						Resource: "serviceaccounts/token",
					},
					Verbs: []string{"create"},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer")
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Resource: "serviceaccounts",
							},
							Verbs: []string{"get", "list"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{providerSAClaimLabel: "true"},
							},
						},
					},
				},
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Resource: "serviceaccounts/token",
							},
							Verbs: []string{"create"},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for VW URL in APIExportES")
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	vwClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Log("Verify the ServiceAccount is visible through the VW")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		vwServiceAccounts, err := vwClient.CoreV1().ServiceAccounts().List(t.Context(), metav1.ListOptions{})
		require.NoError(c, err)
		require.Len(c, vwServiceAccounts.Items, 1, "expect listing exactly one ServiceAccount through the VW")
		require.Equal(c, vwServiceAccounts.Items[0].Name, claimedSA.Name, "expect the ServieAccount to have the name %q", claimedSA.Name)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Run("Test a minted token for the claimed ServiceAccount", func(t *testing.T) {
		t.Parallel()
		t.Log("Mint a token for the ServiceAccount")
		tokenRequest := &authenticationv1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      claimedSA.Name,
				Namespace: claimedSA.Namespace,
			},
		}
		trResponse, err := vwClient.CoreV1().ServiceAccounts().Cluster(consumerClusterName.Path()).Namespace(claimedSA.Namespace).CreateToken(t.Context(), claimedSA.Name, tokenRequest, metav1.CreateOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, trResponse.Status.Token)

		t.Log("Create a new client with the ServiceAccount identity")
		saCfg := framework.ConfigWithToken(trResponse.Status.Token, server.BaseConfig(t))
		saClusterClient, err := kcpkubernetesclientset.NewForConfig(saCfg)
		require.NoError(t, err)

		t.Log("Get test ConfigMap using the ServiceAccount identity")
		saConfigMap, err := saClusterClient.Cluster(consumerPath).CoreV1().ConfigMaps(cm.Namespace).Get(t.Context(), cm.Name, metav1.GetOptions{})
		require.NoError(t, err)

		val := saConfigMap.Data[randomStringKey]
		assert.Equal(t, randomString, val, "expect data to match random test string")
	})

	t.Run("Verify that minting a token for the unclaimed ServiceAccount fails", func(t *testing.T) {
		t.Parallel()
		unclaimedTokenRequest := &authenticationv1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      unclaimedSA.Name,
				Namespace: unclaimedSA.Namespace,
			},
		}
		utrResponse, err := vwClient.CoreV1().ServiceAccounts().Cluster(consumerClusterName.Path()).Namespace(unclaimedSA.Namespace).CreateToken(t.Context(), unclaimedSA.Name, unclaimedTokenRequest, metav1.CreateOptions{})
		assert.True(t, apierrors.IsNotFound(err))
		assert.Empty(t, utrResponse.Status.Token)
	})
}

func TestMintServiceAccountTokenThroughVWFailsWithoutSubresoureClaim(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	const providerSAClaimLabel = "custom.provider/label"

	t.Log("Setup a claimed ServiceAccount in consumer")
	claimedSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claimed-sa",
			Namespace: "default",
			Labels: map[string]string{
				providerSAClaimLabel: "true",
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).CoreV1().ServiceAccounts("default").Create(t.Context(), claimedSA, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create APIExport in provider with claims for ServiceAccount but no subresources")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sa-token",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Resource: "serviceaccounts",
					},
					Verbs: []string{"get", "list"},
					DefaultSelector: &apisv1alpha2.PermissionClaimSelector{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								providerSAClaimLabel: "true",
							},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer")
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Resource: "serviceaccounts",
							},
							Verbs: []string{"get", "list"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{providerSAClaimLabel: "true"},
							},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for VW URL in APIExportES")
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	vwClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Log("Verify the ServiceAccount is visible through the VW")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		vwServiceAccounts, err := vwClient.CoreV1().ServiceAccounts().List(t.Context(), metav1.ListOptions{})
		require.NoError(c, err)
		require.Len(c, vwServiceAccounts.Items, 1, "expect listing exactly one ServiceAccount through the VW")
		require.Equal(c, vwServiceAccounts.Items[0].Name, claimedSA.Name, "expect the ServieAccount to have the name %q", claimedSA.Name)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify mint a token for the ServiceAccount fails")
	tokenRequest := &authenticationv1.TokenRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimedSA.Name,
			Namespace: claimedSA.Namespace,
		},
	}
	trResponse, err := vwClient.CoreV1().ServiceAccounts().Cluster(consumerClusterName.Path()).Namespace(claimedSA.Namespace).CreateToken(t.Context(), claimedSA.Name, tokenRequest, metav1.CreateOptions{})
	require.Error(t, err)
	require.Empty(t, trResponse.Status.Token)
}
