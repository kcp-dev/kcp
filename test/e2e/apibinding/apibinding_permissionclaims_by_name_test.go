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
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	"github.com/kcp-dev/logicalcluster/v2"
	v1 "k8s.io/api/core/v1"
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

// requirements:
//		kcp server X
//		2 workspaces X
//		2 APIs, so that one can be claimed by name. One should be namespaced. X
// tests:
//		when only `name` is specified, exactly one cluster-scoped resource is claimed
//		when only `namespace` is specified, all resources in specified namespace are claimed
//		when both `name` and `namespace` are specified, only named resource in the namespace is claimed
//		when neither `name` nor `namespace` are specified, all resources in the workspace are claimed
//		when `all` and `resourceSelector` are specified, there's an error
func TestPermissionClaimsByName(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	_ = consumerWorkspace

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	//dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	//require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Installing a sheriff APIResourceSchema and APIExport into workspace %q", serviceProviderWorkspace)
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderWorkspace, kcpClusterClient, "wild.wild.west", "board the wanderer")

	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := kcpClusterClient.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
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
	t.Logf("Found identity hash: %v", identityHash)

	t.Logf("Creating namespace")

	ns1, err := kubeClusterClient.Cluster(consumerWorkspace).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ns-1",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ns-1")

	t.Logf("Waiting for namespace to exist")
	framework.Eventually(t, func() (done bool, str string) {
		ns1, err := kubeClusterClient.Cluster(consumerWorkspace).CoreV1().Namespaces().Get(ctx, ns1.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if ns1.Status.Phase == v1.NamespaceActive {
			return true, ""
		}

		return false, "not done waiting for ns1 to be created"
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for namespace to be ready")
	t.Logf("Namespace %s ready", ns1.Name)

	var claimableConfigMaps []*v1.ConfigMap
	for _, name := range []string{"cm-1", "cm-2", "cm-3"} {
		cm, err := createClaimableObject(ctx, t, kubeClusterClient, consumerWorkspace, ns1.Name, name)
		require.NoError(t, err, "could not create ConfigMap %s in namespace %s", name, ns1.Name)
		t.Logf("configmap: %+v", cm)
		claimableConfigMaps = append(claimableConfigMaps, cm)
	}

	createAPIBindingToObject(ctx, t, kcpClusterClient, cfg, serviceProviderWorkspace, consumerWorkspace, claimableConfigMaps, identityHash)
}

func createClaimableObject(ctx context.Context, t *testing.T, kubeClusterClient kcpkubernetesclientset.ClusterInterface, cluster logicalcluster.Name, namespace, name string) (*v1.ConfigMap, error) {
	t.Logf("Creating ConfigMap %s", name)
	// TODO: wait for it to be ready, really
	cm, err := kubeClusterClient.Cluster(cluster).CoreV1().ConfigMaps(namespace).Create(ctx, &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	framework.Eventually(t, func() (done bool, str string) {
		cm, err := kubeClusterClient.Cluster(cluster).CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if cm.UID != "" {
			return true, ""
		}

		return false, "not done waiting for ConfigMap"
	}, wait.ForeverTestTimeout, 110*time.Millisecond, "could not wait for configmap %s to exist", name)
	t.Logf("Done waiting for ConfigMap %s", name)
	return cm, nil
}

// bind to the sheriff API as well as a specified configmaps
func createAPIBindingToObject(ctx context.Context, t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, cfg *rest.Config, providerWorkspace, consumerWorkspace logicalcluster.Name, configMaps []*v1.ConfigMap, identityHash string) {
	t.Logf("Create an APIBinding in consumer workspace %q that points to the sheriffs export from %q", consumerWorkspace, providerWorkspace)

	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       providerWorkspace.String(),
					ExportName: "wild.wild.west",
				},
			},
			PermissionClaims: makeConfigMapPermissionClaims(t, configMaps, identityHash),
		},
	}

	_, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err, "could not create APIBinding")

	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Make sure wild.wild.west API group shows up in consumer workspace %s group discovery", consumerWorkspace.String())
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
}

func makeConfigMapPermissionClaims(t *testing.T, configMaps []*v1.ConfigMap, identityHash string) []apisv1alpha1.AcceptablePermissionClaim {
	var claims []apisv1alpha1.AcceptablePermissionClaim
	for _, cm := range configMaps {
		c := apisv1alpha1.AcceptablePermissionClaim{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
				// TODO(nrb): possibly simplify so that we create a []resourceSelector and attach it?
				ResourceSelector: []apisv1alpha1.ResourceSelector{
					{
						Name:      cm.Name,
						Namespace: cm.Namespace,
					},
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		}
		claims = append(claims, c)
		t.Logf("claim: %+v", c.PermissionClaim.ResourceSelector)
	}

	claims = append(claims, apisv1alpha1.AcceptablePermissionClaim{
		PermissionClaim: apisv1alpha1.PermissionClaim{
			GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			IdentityHash:  identityHash,
			All:           true,
		},
		State: apisv1alpha1.ClaimAccepted,
	})

	return claims
}
