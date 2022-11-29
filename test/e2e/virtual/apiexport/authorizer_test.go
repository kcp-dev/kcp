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

package apiexport

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportAuthorizers(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	org := framework.NewOrganizationFixture(t, server)

	// see https://docs.google.com/drawings/d/1_sOiFZReAfypuUDyHS9rwpxbZgJNJuxdvbgXXgu2KAQ/edit for topology
	serviceProvider1Workspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("provider-a"))
	serviceProvider2Workspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("provider-b"))
	tenantWorkspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("tenant"))
	tenantShadowCRDWorkspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("tenant-shadowed-crd"))

	cfg := server.BaseConfig(t)

	user1KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	user2KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	user3KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	user2DynamicClusterClient, err := kcpdynamic.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	user3DynamicClusterClient, err := kcpdynamic.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, org, []string{"user-1", "user-2", "user-3"}, nil, false)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, serviceProvider1Workspace, []string{"user-1"}, nil, true)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, serviceProvider2Workspace, []string{"user-2"}, nil, true)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, tenantWorkspace, []string{"user-3"}, nil, true)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClient, tenantShadowCRDWorkspace, []string{"user-3"}, nil, true)

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProvider1Workspace, user1KcpClient, "wild.wild.west", "")

	t.Logf("Setting maximal permission policy on APIExport %s|%s", serviceProvider1Workspace, "wild.wild.west")
	export, err := user1KcpClient.Cluster(serviceProvider1Workspace).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport %s|%s", serviceProvider1Workspace, export.Name)

	patchedExport := export.DeepCopy()
	patchedExport.Spec.MaximalPermissionPolicy = &apisv1alpha1.MaximalPermissionPolicy{Local: &apisv1alpha1.LocalAPIExportPolicy{}}
	mergePatch, err := jsonpatch.CreateMergePatch(encodeJSON(t, export), encodeJSON(t, patchedExport))
	require.NoError(t, err)
	_, err = user1KcpClient.Cluster(serviceProvider1Workspace).ApisV1alpha1().APIExports().Patch(ctx, export.Name, types.MergePatchType, mergePatch, metav1.PatchOptions{})
	require.NoError(t, err, "error patching APIExport %s|%s", serviceProvider1Workspace, export.Name)

	t.Logf("get the sheriffs apiexport's generated identity hash")
	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := user1KcpClient.Cluster(serviceProvider1Workspace).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while waiting to get API export: %v", err)
		}
		if conditions.IsTrue(sheriffExport, apisv1alpha1.APIExportIdentityValid) {
			identityHash = sheriffExport.Status.IdentityHash
			return true, ""
		}
		return false, fmt.Sprintf("waiting for API export identity to be valid: %+v", conditions.Get(sheriffExport, apisv1alpha1.APIExportIdentityValid))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be valid with identity hash")
	t.Logf("Found identity hash: %v", identityHash)

	t.Logf("grant user-3 to be able to bind sherriffs API export \"wild.wild.west\" from workspace %q", serviceProvider1Workspace)
	cr, crb := createClusterRoleAndBindings(
		"user-3-bind",
		"user-3", "User",
		[]string{"bind"},
		"apis.kcp.dev", "apiexports", "wild.wild.west",
	)
	_, err = kubeClient.Cluster(serviceProvider1Workspace).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider1Workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
	// create API binding in tenant workspace pointing to the sherriffs export
	apifixtures.BindToExport(ctx, t, serviceProvider1Workspace, "wild.wild.west", tenantWorkspace, user3KcpClient)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProvider2Workspace)
	user2serviceProvider2KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(user2serviceProvider2KcpClient.Cluster(serviceProvider2Workspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, user2DynamicClusterClient.Cluster(serviceProvider2Workspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for today cowboys APIResourceSchema in service provider %q", serviceProvider2Workspace)
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
					All:           true,
				},
				{
					GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
					IdentityHash:  identityHash,
					All:           true,
				},
			},
		},
	}
	_, err = user2KcpClient.Cluster(serviceProvider2Workspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("grant user-3 to be able to bind cowboys API export \"today-cowboys\" from workspace %q", serviceProvider2Workspace)
	cr, crb = createClusterRoleAndBindings(
		"user-3-bind",
		"user-3", "User",
		[]string{"bind"},
		"apis.kcp.dev", "apiexports", "today-cowboys",
	)
	_, err = kubeClient.Cluster(serviceProvider2Workspace).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider2Workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", tenantWorkspace, serviceProvider2Workspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       serviceProvider2Workspace.String(),
					ExportName: "today-cowboys",
				},
			},
		},
	}
	framework.Eventually(t, func() (bool, string) {
		_, err = user3KcpClient.Cluster(tenantWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("Make sure [%q, %q] API groups shows up in consumer workspace %q group discovery", wildwest.GroupName, "wild.wild.west", tenantWorkspace)
	user3tenantWorkspaceKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := user3tenantWorkspaceKcpClient.Cluster(tenantWorkspace).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", tenantWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName) && groupExists(groups, "wild.wild.west"), nil
	})
	require.NoError(t, err)

	t.Logf("Install cowboys CRD into tenant workspace %q", tenantShadowCRDWorkspace)
	user3tenantShadowCRDKubeClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(user3tenantShadowCRDKubeClient.Cluster(tenantShadowCRDWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, user3DynamicClusterClient.Cluster(tenantShadowCRDWorkspace), mapper, nil, "crd_cowboys.yaml", testFiles)
	require.NoError(t, err)

	user3APIExtensionsClient, err := kcpapiextensionsclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	framework.Eventually(t, func() (bool, string) {
		cowboysCRD, err := user3APIExtensionsClient.Cluster(tenantShadowCRDWorkspace).ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "cowboys.wildwest.dev", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		if apihelpers.IsCRDConditionTrue(cowboysCRD, apiextensionsv1.Established) {
			return true, ""
		}
		return false, "waiting for cowboys CRD to become established"
	}, wait.ForeverTestTimeout, time.Millisecond*100, "waiting for cowboys CRD to become established failed")

	apiBinding = &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       serviceProvider2Workspace.String(),
					ExportName: "today-cowboys",
				},
			},
		},
	}
	t.Logf("Create an APIBinding %q in consumer workspace %q that points to the today-cowboys export from %q but shadows a local cowboys CRD at the same time", apiBinding.Name, tenantShadowCRDWorkspace, serviceProvider2Workspace)
	framework.Eventually(t, func() (bool, string) {
		_, err := user3KcpClient.Cluster(tenantShadowCRDWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("Waiting for APIBinding %q in consumer workspace %q to have the condition %q mentioning the conflict with the shadowing local cowboys CRD", apiBinding.Name, tenantShadowCRDWorkspace, apisv1alpha1.BindingUpToDate)
	framework.Eventually(t, func() (bool, string) {
		binding, err := user3KcpClient.Cluster(tenantShadowCRDWorkspace).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		condition := conditions.Get(binding, apisv1alpha1.BindingUpToDate)
		if condition == nil {
			return false, "binding condition not found"
		}
		if strings.Contains(condition.Message, `overlaps with "cowboys.wildwest.dev" CustomResourceDefinition`) {
			return true, ""
		}
		return false, fmt.Sprintf("CRD conflict condition not yet met: %q", condition.Message)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	user3wildwestClusterClient, err := wildwestclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err, "failed to construct wildwest cluster client for server")
	cowboy := newCowboy("default", "cowboy1")

	t.Logf("Creating a cowboy resource available via API binding in consumer workspace %q", tenantWorkspace)
	_, err = user3wildwestClusterClient.Cluster(tenantWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in tenant workspace %q", tenantWorkspace)

	t.Logf("Creating a cowboy resource available via CRD in consumer workspace %q", tenantShadowCRDWorkspace)
	_, err = user3wildwestClusterClient.Cluster(tenantShadowCRDWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in shadowing CRD tenant workspace %q", tenantShadowCRDWorkspace)

	t.Logf("get virtual workspace client for \"today-cowboys\" APIExport in workspace %q", serviceProvider2Workspace)
	var apiExport *apisv1alpha1.APIExport
	framework.Eventually(t, func() (bool, string) {
		var err error
		apiExport, err = user2KcpClient.Cluster(serviceProvider2Workspace).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		if len(apiExport.Status.VirtualWorkspaces) > 0 {
			return true, ""
		}
		return false, "waiting on virtual workspace to be ready"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	user2ApiExportVWCfg := framework.UserConfig("user-2", rest.CopyConfig(cfg))
	user2ApiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL
	user2DynamicVWClient, err := kcpdynamic.NewForConfig(user2ApiExportVWCfg)

	t.Logf("verify that user-2 cannot list sherrifs resources via virtual apiexport apiserver because we have no local maximal permissions yet granted")
	_, err = user2DynamicVWClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"}).List(ctx, metav1.ListOptions{})
	require.ErrorContains(
		t, err,
		`sheriffs.wild.wild.west is forbidden: User "user-2" cannot list resource "sheriffs" in API group "wild.wild.west" at the cluster scope: access denied`,
		"user-2 must not be allowed to list sheriff resources")

	_, err = user2DynamicVWClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "user-2 must be allowed to list native types")

	t.Logf("grant access to sherrifs for %q in workspace %q", apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix+"user-2", serviceProvider1Workspace)
	cr, crb = createClusterRoleAndBindings(
		"apiexport-claimed",
		"apis.kcp.dev:binding:user-2", "User",
		[]string{"create", "list"},
		"wild.wild.west", "sheriffs", "",
	)
	_, err = kubeClient.Cluster(serviceProvider1Workspace).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider1Workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("verify that user-2 can lists all claimed resources using a wildcard request")
	claimedGVRs := []schema.GroupVersionResource{
		{Version: "v1", Resource: "configmaps"},
		{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"},
	}
	framework.Eventually(t, func() (success bool, reason string) {
		for _, gvr := range claimedGVRs {
			_, err := user2DynamicVWClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("error while waiting to list %q: %v", gvr, err)
			}
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "listing claimed resources failed")

	t.Logf("verify that user-2 can lists sherriffs resources in the tenant workspace %q via the virtual apiexport apiserver", tenantWorkspace)
	framework.Eventually(t, func() (success bool, reason string) {
		_, err = user2DynamicVWClient.Cluster(tenantWorkspace).Resource(schema.GroupVersionResource{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"}).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while waiting to list sherriffs: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "listing claimed resources failed")

	t.Logf("verify that user-2 cannot lists CRD shadowed sherriffs resources in the tenant workspace %q via the virtual apiexport apiserver", tenantShadowCRDWorkspace)
	_, err = user2DynamicVWClient.Cluster(tenantShadowCRDWorkspace).Resource(schema.GroupVersionResource{Version: "v1alpha1", Resource: "cowboys", Group: "wildwest.dev"}).List(ctx, metav1.ListOptions{})
	require.Error(t, err, "expected error, got none")
	require.True(t, errors.IsNotFound(err))
}
