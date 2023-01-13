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
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	"github.com/kcp-dev/kcp/pkg/apis/scheduling"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	// see https://docs.google.com/drawings/d/1_sOiFZReAfypuUDyHS9rwpxbZgJNJuxdvbgXXgu2KAQ/edit for topology
	serviceProvider1Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("provider-a"))
	serviceProvider2Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("provider-b"))
	tenantPath, tenantWorkspace := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("tenant"))
	tenantClusterName := logicalcluster.Name(tenantWorkspace.Spec.Cluster)
	tenantShadowCRDPath, tenantShadowCRDWorkspace := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("tenant-shadowed-crd"))
	tenantShadowCRDClusterName := logicalcluster.Name(tenantShadowCRDWorkspace.Spec.Cluster)

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

	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, orgPath, []string{"user-1", "user-2", "user-3"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, serviceProvider1Path, []string{"user-1"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, serviceProvider2Path, []string{"user-2"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, tenantPath, []string{"user-3"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, tenantShadowCRDPath, []string{"user-3"}, nil, true)

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProvider1Path, user1KcpClient, "wild.wild.west", "")

	t.Logf("Setting maximal permission policy on APIExport %s|%s", serviceProvider1Path, "wild.wild.west")
	export, err := user1KcpClient.Cluster(serviceProvider1Path).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport %s|%s", serviceProvider1Path, export.Name)

	patchedExport := export.DeepCopy()
	patchedExport.Spec.MaximalPermissionPolicy = &apisv1alpha1.MaximalPermissionPolicy{Local: &apisv1alpha1.LocalAPIExportPolicy{}}
	mergePatch, err := jsonpatch.CreateMergePatch(encodeJSON(t, export), encodeJSON(t, patchedExport))
	require.NoError(t, err)
	_, err = user1KcpClient.Cluster(serviceProvider1Path).ApisV1alpha1().APIExports().Patch(ctx, export.Name, types.MergePatchType, mergePatch, metav1.PatchOptions{})
	require.NoError(t, err, "error patching APIExport %s|%s", serviceProvider1Path, export.Name)

	t.Logf("get the sheriffs apiexport's generated identity hash")
	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := user1KcpClient.Cluster(serviceProvider1Path).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
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

	t.Logf("grant user-3 to be able to bind sherriffs API export \"wild.wild.west\" from workspace %q", serviceProvider1Path)
	cr, crb := createClusterRoleAndBindings(
		"user-3-bind",
		"user-3", "User",
		[]string{"bind"},
		"apis.kcp.io", "apiexports", "wild.wild.west",
	)
	_, err = kubeClient.Cluster(serviceProvider1Path).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider1Path).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
	// create API binding in tenant workspace pointing to the sherriffs export
	apifixtures.BindToExport(ctx, t, serviceProvider1Path, "wild.wild.west", tenantPath, user3KcpClient)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProvider2Path)
	user2serviceProvider2KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(user2serviceProvider2KcpClient.Cluster(serviceProvider2Path).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, user2DynamicClusterClient.Cluster(serviceProvider2Path), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for today cowboys APIResourceSchema in service provider %q", serviceProvider2Path)
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
	_, err = user2KcpClient.Cluster(serviceProvider2Path).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("grant user-3 to be able to bind cowboys API export \"today-cowboys\" from workspace %q", serviceProvider2Path)
	cr, crb = createClusterRoleAndBindings(
		"user-3-bind",
		"user-3", "User",
		[]string{"bind"},
		"apis.kcp.io", "apiexports", "today-cowboys",
	)
	_, err = kubeClient.Cluster(serviceProvider2Path).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider2Path).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", tenantPath, serviceProvider2Path)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: serviceProvider2Path.String(),
					Name: "today-cowboys",
				},
			},
		},
	}
	framework.Eventually(t, func() (bool, string) {
		_, err = user3KcpClient.Cluster(tenantPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("Make sure [%q, %q] API groups shows up in consumer workspace %q group discovery", wildwest.GroupName, "wild.wild.west", tenantPath)
	user3tenantWorkspaceKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := user3tenantWorkspaceKcpClient.Cluster(tenantPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", tenantPath, err)
		}
		return groupExists(groups, wildwest.GroupName) && groupExists(groups, "wild.wild.west"), nil
	})
	require.NoError(t, err)

	t.Logf("Install cowboys CRD into tenant workspace %q", tenantShadowCRDPath)
	user3tenantShadowCRDKubeClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(user3tenantShadowCRDKubeClient.Cluster(tenantShadowCRDPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, user3DynamicClusterClient.Cluster(tenantShadowCRDPath), mapper, nil, "crd_cowboys.yaml", testFiles)
	require.NoError(t, err)

	user3APIExtensionsClient, err := kcpapiextensionsclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err)
	framework.Eventually(t, func() (bool, string) {
		cowboysCRD, err := user3APIExtensionsClient.Cluster(tenantShadowCRDPath).ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "cowboys.wildwest.dev", metav1.GetOptions{})
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
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: serviceProvider2Path.String(),
					Name: "today-cowboys",
				},
			},
		},
	}
	t.Logf("Create an APIBinding %q in consumer workspace %q that points to the today-cowboys export from %q but shadows a local cowboys CRD at the same time", apiBinding.Name, tenantShadowCRDPath, serviceProvider2Path)
	framework.Eventually(t, func() (bool, string) {
		_, err := user3KcpClient.Cluster(tenantShadowCRDPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("Waiting for APIBinding %q in consumer workspace %q to have the condition %q mentioning the conflict with the shadowing local cowboys CRD", apiBinding.Name, tenantShadowCRDPath, apisv1alpha1.BindingUpToDate)
	framework.Eventually(t, func() (bool, string) {
		binding, err := user3KcpClient.Cluster(tenantShadowCRDPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
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

	t.Logf("Creating a cowboy resource available via API binding in consumer workspace %q", tenantPath)
	_, err = user3wildwestClusterClient.Cluster(tenantPath).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in tenant workspace %q", tenantPath)

	t.Logf("Creating a cowboy resource available via CRD in consumer workspace %q", tenantShadowCRDPath)
	_, err = user3wildwestClusterClient.Cluster(tenantShadowCRDPath).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in shadowing CRD tenant workspace %q", tenantShadowCRDPath)

	t.Logf("get virtual workspace client for \"today-cowboys\" APIExport in workspace %q", serviceProvider2Path)
	var apiExport *apisv1alpha1.APIExport
	framework.Eventually(t, func() (bool, string) {
		var err error
		apiExport, err = user2KcpClient.Cluster(serviceProvider2Path).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		if len(apiExport.Status.VirtualWorkspaces) > 0 {
			return true, ""
		}
		return false, "waiting on virtual workspace to be ready"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	user2ApiExportVWCfg := framework.UserConfig("user-2", rest.CopyConfig(cfg))
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
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

	t.Logf("grant access to sherrifs for %q in workspace %q", apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix+"user-2", serviceProvider1Path)
	cr, crb = createClusterRoleAndBindings(
		"apiexport-claimed",
		"apis.kcp.io:binding:user-2", "User",
		[]string{"create", "list"},
		"wild.wild.west", "sheriffs", "",
	)
	_, err = kubeClient.Cluster(serviceProvider1Path).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(serviceProvider1Path).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
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

	t.Logf("verify that user-2 can lists sherriffs resources in the tenant workspace %q via the virtual apiexport apiserver", tenantPath)
	framework.Eventually(t, func() (success bool, reason string) {
		_, err = user2DynamicVWClient.Cluster(tenantClusterName.Path()).Resource(schema.GroupVersionResource{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"}).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while waiting to list sherriffs: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "listing claimed resources failed")

	t.Logf("verify that user-2 cannot lists CRD shadowed sherriffs resources in the tenant workspace %q via the virtual apiexport apiserver", tenantShadowCRDPath)
	_, err = user2DynamicVWClient.Cluster(tenantShadowCRDClusterName.Path()).Resource(schema.GroupVersionResource{Version: "v1alpha1", Resource: "cowboys", Group: "wildwest.dev"}).List(ctx, metav1.ListOptions{})
	require.Error(t, err, "expected error, got none")
	require.True(t, errors.IsNotFound(err))
}

func TestRootAPIExportAuthorizers(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	servicePath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("provider"))
	userPath, userWorkspace := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer"))
	userClusterName := logicalcluster.Name(userWorkspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)
	kcpClient, err := kcpclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	providerUser := "user-1"
	consumerUser := "user-2"

	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, orgPath, []string{providerUser, consumerUser}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, servicePath, []string{providerUser}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, userPath, []string{consumerUser}, nil, true)

	serviceKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig(providerUser, rest.CopyConfig(cfg)))
	require.NoError(t, err)
	serviceDynamicClusterClient, err := kcpdynamic.NewForConfig(framework.UserConfig(providerUser, rest.CopyConfig(cfg)))
	require.NoError(t, err)

	userKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig(consumerUser, rest.CopyConfig(cfg)))
	require.NoError(t, err)

	t.Logf("Install APIResourceSchema into service provider workspace %q", servicePath)
	serviceProviderKcpClient, err := kcpclientset.NewForConfig(framework.UserConfig(providerUser, rest.CopyConfig(cfg)))
	require.NoError(t, err)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderKcpClient.Cluster(servicePath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, serviceDynamicClusterClient.Cluster(servicePath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Get the root scheduling APIExport's identity hash")
	schedulingAPIExport, err := kcpClient.Cluster(core.RootCluster.Path()).ApisV1alpha1().APIExports().Get(ctx, "scheduling.kcp.io", metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, conditions.IsTrue(schedulingAPIExport, apisv1alpha1.APIExportIdentityValid))
	identityHash := schedulingAPIExport.Status.IdentityHash
	require.NotNil(t, identityHash)

	t.Logf("Create an APIExport for APIResourceSchema in service provider %q", servicePath)
	apiExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{Group: scheduling.GroupName, Resource: "placements"},
					IdentityHash:  identityHash,
					All:           true,
				},
			},
		},
	}
	apiExport, err = serviceKcpClient.Cluster(servicePath).ApisV1alpha1().APIExports().Create(ctx, apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Grant user to be able to bind service API export from workspace %q", servicePath)
	cr, crb := createClusterRoleAndBindings(
		consumerUser,
		consumerUser, "User",
		[]string{"bind"},
		apis.GroupName, "apiexports", apiExport.Name,
	)
	_, err = kubeClient.Cluster(servicePath).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.Cluster(servicePath).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the service APIExport from %q", userPath, servicePath)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: servicePath.String(),
					Name: apiExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{Group: scheduling.GroupName, Resource: "placements"},
						IdentityHash:  identityHash,
						All:           true,
					},
					State: apisv1alpha1.ClaimAccepted,
				},
			},
		},
	}
	framework.Eventually(t, func() (bool, string) {
		_, err := userKcpClient.Cluster(userPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating API binding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "api binding creation failed")

	t.Logf("Wait for the binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := userKcpClient.Cluster(userPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
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

	t.Logf("Get virtual workspace client for service APIExport in workspace %q", servicePath)
	var export *apisv1alpha1.APIExport
	framework.Eventually(t, func() (bool, string) {
		var err error
		export, err = serviceKcpClient.Cluster(servicePath).ApisV1alpha1().APIExports().Get(ctx, apiExport.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on APIExport to be available %v", err.Error())
		}
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		if len(export.Status.VirtualWorkspaces) > 0 {
			return true, ""
		}
		return false, "waiting on virtual workspace to be ready"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	serviceAPIExportVWCfg := framework.UserConfig(providerUser, rest.CopyConfig(cfg))
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	serviceAPIExportVWCfg.Host = export.Status.VirtualWorkspaces[0].URL
	serviceDynamicVWClient, err := kcpdynamic.NewForConfig(serviceAPIExportVWCfg)
	require.NoError(t, err)

	t.Logf("Verify that service user can create a claimed resource in user workspace")
	placement := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": schedulingv1alpha1.SchemeGroupVersion.String(),
			"kind":       "Placement",
			"metadata": map[string]interface{}{
				"name": "default",
			},
			"spec": map[string]interface{}{
				"locationResource": map[string]interface{}{
					"group":    workloadv1alpha1.SchemeGroupVersion.Group,
					"resource": "synctargets",
					"version":  workloadv1alpha1.SchemeGroupVersion.Version,
				},
			},
		},
	}
	_, err = serviceDynamicVWClient.Cluster(userClusterName.Path()).
		Resource(schedulingv1alpha1.SchemeGroupVersion.WithResource("placements")).
		Create(ctx, placement, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Verify that consumer user can get the created resource in user workspace")
	_, err = userKcpClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placement.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
}
