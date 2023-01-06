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

package apibinding

import (
	"context"
	"fmt"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/logicalcluster/v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
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

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	t.Logf("Installing a sheriff APIResourceSchema and APIExport into workspace %q", serviceProviderWorkspace)
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderWorkspace.Path(), kcpClusterClient, "wild.wild.west", "board the wanderer")

	sheriffExport := &apisv1alpha1.APIExport{}
	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if conditions.IsTrue(sheriffExport, apisv1alpha1.APIExportIdentityValid) {
			identityHash = sheriffExport.Status.IdentityHash
			return true, ""
		}
		condition := conditions.Get(sheriffExport, apisv1alpha1.APIExportIdentityValid)
		if condition != nil {
			return false, fmt.Sprintf("not done waiting for API Export condition status:%v - reason: %v - message: %v", condition.Status, condition.Reason, condition.Message)
		}
		return false, "not done waiting for APIExportIdentity to be marked valid, no condition exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be valid with identity hash")
	require.NotNil(t, sheriffExport)

	t.Logf("Found identity hash: %v", identityHash)

	t.Logf("Creating consumer namespace")
	consumerNS1, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "consumer-ns-1",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ns-1")

	t.Logf("Waiting for namespace to exist")
	framework.Eventually(t, func() (done bool, str string) {
		consumerNS1, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Namespaces().Get(ctx, consumerNS1.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if consumerNS1.Status.Phase == v1.NamespaceActive {
			return true, ""
		}

		return false, "not done waiting for ns1 to be created"
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for namespace to be ready")
	t.Logf("Namespace %s ready", consumerNS1.Name)

	t.Logf("setting PermissionClaims on APIExport %s", sheriffExport.Name)
	sheriffExport.Spec.PermissionClaims = makeNarrowCMPermissionClaims("", "consumer-ns-1")
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err = kcpClusterClient.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Update(ctx, sheriffExport, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}

		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be updated with PermissionClaims")

	t.Logf("binding consumer to provider export")
	binding := bindConsumerToProviderCMExport(ctx, t, consumerWorkspace.Path(), serviceProviderWorkspace, kcpClusterClient, identityHash, "", consumerNS1.Name)
	require.NotNil(t, binding)

	apiExportVWCfg := rest.CopyConfig(cfg)
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	apiExportVWCfg.Host = sheriffExport.Status.VirtualWorkspaces[0].URL
	t.Logf("vwHost: %s", apiExportVWCfg.Host)

	apiExportClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Logf("verify we can create a new configmap in consumer namespace via the virtual workspace")
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "confmap1",
		},
	}
	framework.Eventually(t, func() (done bool, str string) {
		cm, err = apiExportClient.Cluster(consumerWorkspace.Path()).CoreV1().ConfigMaps(consumerNS1.Name).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}

		return true, ""
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "timed out trying to create configmap in consumer namespace")
	require.Equal(t, "consumer-ns-1", cm.Namespace)
	t.Logf("cluster for CM: %s", logicalcluster.From(cm).String())

	t.Logf("verify we can update a configmap in consumer workspace via the virtual workspace")
	cm.Data = map[string]string{
		"something": "new",
	}
	framework.Eventually(t, func() (done bool, str string) {
		cm, err = apiExportClient.Cluster(consumerWorkspace.Path()).CoreV1().ConfigMaps(consumerNS1.Name).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}

		return true, ""
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "timed out trying to update configmap in consumer namespace")
	require.Equal(t, cm.Data["something"], "new")

	t.Logf("ensure that configmaps in an unspecified namespace cannot be created")

	t.Logf("Creating unclaimed consumer namespace")
	consumerNS2, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "consumer-ns-2",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create %s", consumerNS2.Name)

	t.Logf("Waiting for namespace to exist")
	framework.Eventually(t, func() (done bool, str string) {
		consumerNS1, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Namespaces().Get(ctx, consumerNS2.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if consumerNS1.Status.Phase == v1.NamespaceActive {
			return true, ""
		}

		return false, "not done waiting for ns2 to be created"
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for namespace to be ready")
	t.Logf("Namespace %s ready", consumerNS2.Name)

	cm = &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "confmap2",
		},
	}
	framework.Eventually(t, func() (done bool, str string) {
		// TODO(nrb): This create should be forbidden. However, constructing a URL manually allows creation
		cm, err = apiExportClient.Cluster(consumerWorkspace.Path()).CoreV1().ConfigMaps(consumerNS2.Name).Create(ctx, cm, metav1.CreateOptions{})
		if apierrors.IsForbidden(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}

		return false, "unexpected create"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "never received forbidden error")

	t.Logf("End of test")
}

// makeNarrowCMPermissionClaim creates a PermissionClaim for ConfigMaps scoped to just a name, just a namespace, or both.
func makeNarrowCMPermissionClaims(name, namespace string) []apisv1alpha1.PermissionClaim {
	return []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
			All:           false,
			ResourceSelector: []apisv1alpha1.ResourceSelector{
				{
					Name:      name,
					Namespace: namespace,
				},
			},
		},
	}
}

func makeAcceptedSheriffPermissionClaims(identityHash, name, namespace string) []apisv1alpha1.AcceptablePermissionClaim {
	return []apisv1alpha1.AcceptablePermissionClaim{
		//{
		//	PermissionClaim: apisv1alpha1.PermissionClaim{
		//		GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
		//		IdentityHash:  identityHash,
		//		All:           true,
		//	},
		//	State: apisv1alpha1.ClaimAccepted,
		//},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
				ResourceSelector: []apisv1alpha1.ResourceSelector{
					{
						Name:      name,
						Namespace: namespace,
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
	consumerWorkspace logicalcluster.Path,
	providerClusterName logicalcluster.Name,
	kcpClusterClients kcpclientset.ClusterInterface,
	identityHash, cmName, cmNamespace string,
) *apisv1alpha1.APIBinding {
	t.Helper()
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerClusterName)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-and-configmaps",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerClusterName.Path().String(),
					Name: "wild.wild.west",
				},
			},
			PermissionClaims: makeAcceptedSheriffPermissionClaims(identityHash, cmName, cmNamespace),
		},
	}

	binding := &apisv1alpha1.APIBinding{}
	framework.Eventually(t, func() (bool, string) {
		var err error
		binding, err = kcpClusterClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	return binding
}
