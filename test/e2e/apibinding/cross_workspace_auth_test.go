/*
Copyright 2025 The KCP Authors.

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

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/authfixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestWorkspaceAuth tests that a user from the consumer workspace can
// bind an APIExport from the provider workspace.
//  1. The user is authenticated via OIDC against the consumer workspace
//     using a method that is only available in the consumer workspace
//  2. The user can manage APIBindings in the consumer workspace
//  3. The provider workspace allows the binding of APIExports for users
//     coming from the consumer workspace through the
//     system:cluster:<consumer-cluster> group
func TestWorkspaceAuth(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("apibinding-serviceaccount"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(kcpConfig)
	require.NoError(t, err)

	t.Log("Create consumer workspace with OIDC")
	mockOidc, ca := authfixtures.StartMockOIDC(t, server)
	authConfig := authfixtures.CreateWorkspaceOIDCAuthentication(t, t.Context(), kcpClusterClient, baseWsPath, mockOidc, ca, nil)
	wsOidcType := authfixtures.CreateWorkspaceType(t, t.Context(), kcpClusterClient, baseWsPath, "with-oidc", authConfig)
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("consumer"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsOidcType)))

	t.Log("Create an oidc client")
	token := authfixtures.CreateOIDCToken(t, mockOidc, "test-oidc-user", "testuser@example.com", nil)
	oidcKcpClient, err := kcpclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
	require.NoError(t, err)

	t.Log("Create provider workspace")
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("provider"))

	t.Log("Create APIExport in provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kubeClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Allow users from consumer to bind APIExports through group system:cluster:%s", consumerWS.Spec.Cluster)
	providerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"bind"},
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports"},
				ResourceNames: []string{cowboysAPIExport.Name},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), providerCR, metav1.CreateOptions{})
	require.NoError(t, err)

	providerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:cluster:" + consumerWS.Spec.Cluster,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     providerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), providerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Allow authenticated users to manage APIBindings in consumer")

	consumerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manage-apibindings",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
			},
			{
				Verbs:           []string{"*"},
				NonResourceURLs: []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoles().Create(t.Context(), consumerCR, metav1.CreateOptions{})
	require.NoError(t, err)
	consumerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "authenticated-manage-apibindings",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:authenticated",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     consumerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), consumerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer using the oidc client")
	cowboysAPIBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = oidcKcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysAPIBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerPath)
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := oidcKcpClient.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerPath, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerPath)
	resources, err := kubeClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerPath)
}

// TestServiceAccount tests that a Service Account from the consumer workspace can
// bind an APIExport from the provider workspace.
//  1. The Service Account can manage APIBindings in the consumer workspace
//  2. The provider workspace allows the binding of APIExports for users
//     coming from the consumer workspace through the
//     system:cluster:<consumer-cluster> group
//
// This test is functionally equivalent to TestWorkspaceAuth, but uses
// a Service Account instead of a "real" user authenticated via OIDC.
// Background is that in KCP Service Accounts are sometimes handled
// differently to users and go through a different code path when
// effective users are computed.
func TestServiceAccount(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("apibinding-oidc"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(kcpConfig)
	require.NoError(t, err)

	t.Log("Create consumer workspace")
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("consumer"))

	t.Log("Create a service account in the consumer workspace")
	_, tokenSecret := authfixtures.CreateServiceAccount(t, kubeClusterClient, consumerPath, "default", "test-sa-")
	saClient, err := kcpclientset.NewForConfig(framework.ConfigWithToken(string(tokenSecret.Data["token"]), kcpConfig))
	require.NoError(t, err)

	t.Log("Create provider workspace")
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("provider"))

	t.Log("Create APIExport in provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kubeClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Allow users from consumer to bind APIExports through group system:cluster:%s", consumerWS.Spec.Cluster)
	providerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"bind"},
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports"},
				ResourceNames: []string{cowboysAPIExport.Name},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), providerCR, metav1.CreateOptions{})
	require.NoError(t, err)

	providerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:cluster:" + consumerWS.Spec.Cluster,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     providerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), providerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Allow authenticated users to manage APIBindings in consumer")

	consumerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manage-apibindings",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
			},
			{
				Verbs:           []string{"*"},
				NonResourceURLs: []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoles().Create(t.Context(), consumerCR, metav1.CreateOptions{})
	require.NoError(t, err)
	consumerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "authenticated-manage-apibindings",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:authenticated",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     consumerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), consumerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer using the service account client")
	cowboysAPIBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = saClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysAPIBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerPath)
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := saClient.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerPath, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerPath)
	resources, err := kubeClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerPath)
}

// TestScopedUser tests that a user restricted to the consumer workspace
// by scopes can bind an APIExport from the provider workspace.
//  1. The user is a global KCP user but is restricted by scopes to
//     the consumer workspace
//  2. The user can manage APIBindings in the consumer workspace
//  3. The provider workspace allows the binding of APIExports for users
//     coming from the consumer workspace through the
//     system:cluster:<consumer-cluster> group
//
// This test is functionally equivalent to TestWorkspaceAuth, but
// impersonates a global user with scopes instead of using
// a per-workspace user.
func TestScopedUser(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("apibinding-scoped"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(kcpConfig)
	require.NoError(t, err)

	t.Log("Create consumer workspace")
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("consumer"))

	t.Log("Impersonate an authenticated user restricted to the consumer workspace with scopes")

	t.Log("Admit user-1 as admin to the consumer workspace")
	testUser := "user-1"
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumerPath, []string{testUser}, nil, true)

	t.Log("Admit user-2 as a normal user to the consumer workspace")
	impersonateUser := "user-2"
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumerPath, []string{impersonateUser}, nil, false)

	t.Log("Use user-1 to impersonate user-2 with a scope")
	userCfg := framework.StaticTokenUserConfig(testUser, kcpConfig)
	userCfg.Impersonate = rest.ImpersonationConfig{
		UserName: impersonateUser,
		Extra: map[string][]string{
			rbacregistryvalidation.ScopeExtraKey: {"cluster:" + consumerWS.Spec.Cluster},
		},
	}
	userClient, err := kcpclientset.NewForConfig(userCfg)
	require.NoError(t, err)

	t.Log("Create provider workspace")
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("provider"))

	t.Log("Create APIExport in provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kubeClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Allow users from consumer to bind APIExports through group system:cluster:%s", consumerWS.Spec.Cluster)
	providerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"bind"},
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports"},
				ResourceNames: []string{cowboysAPIExport.Name},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), providerCR, metav1.CreateOptions{})
	require.NoError(t, err)

	providerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:cluster:" + consumerWS.Spec.Cluster,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     providerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), providerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Allow authenticated users to manage APIBindings in consumer")

	consumerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manage-apibindings",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
			},
			{
				Verbs:           []string{"*"},
				NonResourceURLs: []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoles().Create(t.Context(), consumerCR, metav1.CreateOptions{})
	require.NoError(t, err)
	consumerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "authenticated-manage-apibindings",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:authenticated",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     consumerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), consumerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer using the user client")
	cowboysAPIBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = userClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysAPIBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerPath)
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := userClient.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerPath, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerPath)
	resources, err := kubeClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerPath)
}

// TestUserWithWarrants tests that a user with a warrant for another
// user in the workspace can bind an APIExport from the provider
// workspace.
//  1. The user is a global KCP user but can act as another user
//     in consumer workspace through a warrant.
//  2. The user can manage APIBindings in the consumer workspace
//  3. The provider workspace allows the binding of APIExports for users
//     coming from the consumer workspace through the
//     system:cluster:<consumer-cluster> group
//
// This test is functionally equivalent to TestWorkspaceAuth, but
// uses a global user using warrants to act as a scoped user
// a per-workspace user.
func TestUserWithWarrants(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("apibinding-warrants"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(kcpConfig)
	require.NoError(t, err)

	t.Log("Create consumer workspace")
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("consumer"))

	t.Log("Impersonate an authenticated user restricted to the consumer workspace with scopes")

	t.Log("Admit user-1 as admin to the consumer workspace")
	testUser := "user-1"
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumerPath, []string{testUser}, nil, true)

	t.Log("Admit user-2 as a normal user to the consumer workspace")
	impersonateUser := "user-2"
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumerPath, []string{impersonateUser}, nil, false)

	t.Log("Use user-1 to impersonate itself with a warrant for user-2 with a scope")
	userCfg := framework.StaticTokenUserConfig(testUser, kcpConfig)
	userCfg.Impersonate = rest.ImpersonationConfig{
		UserName: testUser,
		Extra: map[string][]string{
			rbacregistryvalidation.WarrantExtraKey: {`{"user":"` + impersonateUser + `","extra":{"authentication.kcp.io/scopes": ["cluster:` + consumerWS.Spec.Cluster + `"]}}`},
		},
	}
	t.Log("user-1 extra", "extra", userCfg.Impersonate.Extra)
	userClient, err := kcpclientset.NewForConfig(userCfg)
	require.NoError(t, err)

	t.Log("Create provider workspace")
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("provider"))

	t.Log("Create APIExport in provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kubeClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Allow users from consumer to bind APIExports through group system:cluster:%s", consumerWS.Spec.Cluster)
	providerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"bind"},
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports"},
				ResourceNames: []string{cowboysAPIExport.Name},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), providerCR, metav1.CreateOptions{})
	require.NoError(t, err)

	providerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bind-apiexport",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:cluster:" + consumerWS.Spec.Cluster,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     providerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), providerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Allow authenticated users to manage APIBindings in consumer")

	consumerCR := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manage-apibindings",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
			},
			{
				Verbs:           []string{"*"},
				NonResourceURLs: []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoles().Create(t.Context(), consumerCR, metav1.CreateOptions{})
	require.NoError(t, err)
	consumerCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "authenticated-manage-apibindings",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "Group",
				Name: "system:authenticated",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     consumerCR.Name,
		},
	}
	_, err = kubeClusterClient.Cluster(consumerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), consumerCRB, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind APIExport in consumer using the user client")
	cowboysAPIBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = userClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysAPIBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerPath)
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := userClient.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerPath, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerPath)
	resources, err := kubeClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerPath)
}
