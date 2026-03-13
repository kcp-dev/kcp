/*
Copyright 2026 The KCP Authors.

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

package customresourcedefinition

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCRDVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	kcpClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	t.Log("Creating cowboys APIExport in provider workspace")
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClient, "wildwest.dev", "Wild West API")

	t.Log("Adding CRD permission claim to APIExport")
	require.Eventually(t, func() bool {
		export, err := kcpClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wildwest.dev", metav1.GetOptions{})
		if err != nil {
			return false
		}

		export.Spec.PermissionClaims = []apisv1alpha2.PermissionClaim{
			{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "apiextensions.k8s.io",
					Resource: "customresourcedefinitions",
				},
				Verbs: []string{"create", "get", "list", "watch", "update", "patch", "delete"},
			},
		}
		export.Spec.MaximalPermissionPolicy = &apisv1alpha2.MaximalPermissionPolicy{
			Local: &apisv1alpha2.LocalAPIExportPolicy{},
		}

		_, err = kcpClient.Cluster(providerPath).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Failed to add CRD permission claim to APIExport")

	t.Log("Creating RBAC rules in provider workspace for maximal permission policy")
	kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	crdRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "kcp-admin-crd-permissions"},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: []string{"create", "get", "list", "watch", "update", "patch", "delete"}},
		},
	}
	_, err = kubeClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(ctx, crdRole, metav1.CreateOptions{})
	require.NoError(t, err)

	crdRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kcp-admin-crd-permissions"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "apis.kcp.io:binding:kcp-admin"}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.SchemeGroupVersion.Group, Kind: "ClusterRole", Name: "kcp-admin-crd-permissions"},
	}
	_, err = kubeClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(ctx, crdRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Binding to cowboys export in consumer workspace with accepted CRD permission claims")
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildwest.dev",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "wildwest.dev",
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "apiextensions.k8s.io",
								Resource: "customresourcedefinitions",
							},
							Verbs: []string{"create", "get", "list", "watch", "update", "patch", "delete"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}

	require.Eventually(t, func() bool {
		_, err := kcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Failed to create APIBinding")

	t.Log("Waiting for APIBinding to be bound")
	require.Eventually(t, func() bool {
		binding, err := kcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "wildwest.dev", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIBinding never reached Bound phase")

	t.Log("Waiting for RBAC to propagate")
	time.Sleep(2 * time.Second)

	t.Log("Getting virtual workspace URL from APIExportEndpointSlice")
	var vwURL string
	require.Eventually(t, func() bool {
		endpointSlice, err := kcpClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "wildwest.dev", metav1.GetOptions{})
		if err != nil {
			t.Logf("Failed to get APIExportEndpointSlice: %v", err)
			return false
		}
		var found bool
		vwURL, found, err = framework.VirtualWorkspaceURL(ctx, kcpClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(endpointSlice))
		if err != nil {
			t.Logf("Error getting virtual workspace URL: %v", err)
			return false
		}
		return found
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIExportEndpointSlice never got virtual workspace URL")

	t.Logf("Virtual workspace URL: %s", vwURL)

	t.Log("Creating apiextensions client for virtual workspace")
	vwCfg := rest.CopyConfig(cfg)
	vwCfg.Host = vwURL
	vwAPIExtensionsClient, err := kcpapiextensionsclientset.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Log("Loading shirts CRD from YAML file")
	shirtsCRDBytes, err := testFiles.ReadFile("shirts_crd.yaml")
	require.NoError(t, err)

	shirtsCRD := &apiextensionsv1.CustomResourceDefinition{}
	err = yaml.Unmarshal(shirtsCRDBytes, shirtsCRD)
	require.NoError(t, err)

	t.Log("Creating shirts CRD via virtual workspace URL")

	created, err := vwAPIExtensionsClient.Cluster(logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path()).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, shirtsCRD, metav1.CreateOptions{})
	require.NoError(t, err, "Failed to create CRD via virtual workspace - OpenAPI schema may be incomplete")
	require.NotNil(t, created)
	t.Logf("Successfully created CRD: %s", created.Name)

	retrieved, err := vwAPIExtensionsClient.Cluster(logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path()).ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "shirts.stable.example.com", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "shirts.stable.example.com", retrieved.Name)
	require.Equal(t, "stable.example.com", retrieved.Spec.Group)
	require.Equal(t, "v1", retrieved.Spec.Versions[0].Name)
	require.Equal(t, ".spec.color", retrieved.Spec.Versions[0].SelectableFields[0].JSONPath)
	require.Equal(t, ".spec.size", retrieved.Spec.Versions[0].SelectableFields[1].JSONPath)
}
