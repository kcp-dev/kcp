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

package authorizer

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestReversePermissionClaimsAuthorizer(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	org := framework.NewOrganizationFixture(t, server)

	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithName("provider"))
	tenantWorkspace := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithName("tenant"))

	cfg := server.BaseConfig(t)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	serviceProviderKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	serviceProviderDynamicKCPClient, err := kcpdynamic.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	serviceProviderKubeClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
	require.NoError(t, err)

	tenantKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	tenantKubeClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, org.Path(), []string{"user-1", "user-2"}, nil, false)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, serviceProviderWorkspace.Path(), []string{"user-1"}, nil, true)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, tenantWorkspace.Path(), []string{"user-2"}, nil, true)

	t.Logf("install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderKcpClient.Cluster(serviceProviderWorkspace.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, serviceProviderDynamicKCPClient.Cluster(serviceProviderWorkspace.Path()), mapper, nil, "apiresourceschema_cowboys.yaml", embeddedResources)
	require.NoError(t, err)

	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
					ResourceSelector: []apisv1alpha1.ResourceSelector{
						{Namespace: "claimed-namespace", Name: "claimed-configmap"},
					},
					Verbs: apisv1alpha1.Verbs{
						Claimed:    []string{"*"},
						RestrictTo: []string{"list", "get", "watch"},
					},
				},
			},
		},
	}
	_, err = serviceProviderKcpClient.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("grant user-2 to be able to bind cowboys API export \"today-cowboys\" from workspace %q", serviceProviderWorkspace)
	cr, crb := createClusterRoleAndBindings(
		"user-2-bind",
		"user-2", "User",
		[]string{"bind"},
		"apis.kcp.io", "apiexports", "today-cowboys",
	)
	_, err = serviceProviderKubeClient.Cluster(serviceProviderWorkspace.Path()).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = serviceProviderKubeClient.Cluster(serviceProviderWorkspace.Path()).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("create an APIBinding in tenant workspace %q that points to the today-cowboys export from %q", tenantWorkspace, serviceProviderWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: serviceProviderWorkspace.String(),
					Name: "today-cowboys",
				},
			},
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
						ResourceSelector: []apisv1alpha1.ResourceSelector{
							{Namespace: "claimed-namespace", Name: "claimed-configmap"},
						},
						Verbs: apisv1alpha1.Verbs{
							Claimed:    []string{"*"},
							RestrictTo: []string{"list", "get", "watch"},
						},
					},
					State: apisv1alpha1.ClaimAccepted,
				},
			},
		},
	}
	framework.Eventually(t, func() (bool, string) {
		_, err = tenantKcpClient.Cluster(tenantWorkspace.Path()).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("wait for API binding %q to have applied permission claims in tenant workspace %q", apiBinding.Name, tenantWorkspace)
	_, err = tenantKubeClient.Cluster(tenantWorkspace.Path()).CoreV1().Namespaces().Create(ctx, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "claimed-namespace"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("create configmap \"claimed-configmap\" in namespace \"claimed-namespace\" in tenant workspace %q", tenantWorkspace)
	_, err = tenantKubeClient.Cluster(tenantWorkspace.Path()).CoreV1().ConfigMaps("claimed-namespace").Create(ctx, &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "claimed-configmap"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("verify that configmap \"claimed-configmap\" in namespace \"claimed-namespace\" in tenant workspace %q can be read", tenantWorkspace)
	_, err = tenantKubeClient.Cluster(tenantWorkspace.Path()).CoreV1().ConfigMaps("claimed-namespace").Get(ctx, "claimed-configmap", metav1.GetOptions{})
	require.NoError(t, err)

	t.Logf("verify that configmap \"claimed-configmap\" in namespace \"claimed-namespace\" in tenant workspace %q cannot be deleted", tenantWorkspace)
	err = tenantKubeClient.Cluster(tenantWorkspace.Path()).CoreV1().ConfigMaps("claimed-namespace").Delete(ctx, "claimed-configmap", metav1.DeleteOptions{})
	require.Error(t, err)

	t.Logf("get virtual workspace client for \"today-cowboys\" APIExport in workspace %q", serviceProviderWorkspace)
	var apiExport *apisv1alpha1.APIExport
	framework.Eventually(t, func() (bool, string) {
		var err error
		apiExport, err = serviceProviderKcpClient.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		if len(apiExport.Status.VirtualWorkspaces) > 0 {
			return true, ""
		}
		return false, "waiting on virtual workspace to be ready"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	serviceProviderAPIExportServiceCfg := framework.UserConfig("user-1", rest.CopyConfig(cfg))
	serviceProviderAPIExportServiceCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL
	serviceProviderAPIExportServiceDynamicClient, err := kcpdynamic.NewForConfig(serviceProviderAPIExportServiceCfg)

	t.Logf("verify that service provider \"user-1\" can get claimed configmap in tenant workspace %q", tenantWorkspace)
	framework.Eventually(t, func() (success bool, reason string) {
		_, err := serviceProviderAPIExportServiceDynamicClient.
			Cluster(tenantWorkspace.Path()).
			Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).
			Namespace("claimed-namespace").
			Get(ctx, "claimed-configmap", metav1.GetOptions{})

		if err != nil {
			return false, fmt.Sprintf("error while waiting to get configmap: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "getting claimed resources failed")

}

func createClusterRoleAndBindings(name, subjectName, subjectKind string, verbs []string, resources ...string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	var rules []rbacv1.PolicyRule

	for i := 0; i < len(resources)/3; i++ {
		group := resources[i*3]
		resource := resources[i*3+1]
		resourceName := resources[i*3+2]

		r := rbacv1.PolicyRule{
			Verbs:     verbs,
			APIGroups: []string{group},
			Resources: []string{resource},
		}

		if resourceName != "" {
			r.ResourceNames = []string{resourceName}
		}

		rules = append(rules, r)
	}

	return &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Rules: rules,
		}, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: subjectKind,
					Name: subjectName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.SchemeGroupVersion.Group,
				Kind:     "ClusterRole",
				Name:     name,
			},
		}
}
