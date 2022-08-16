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

	"github.com/google/go-cmp/cmp"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kcpdynamic "github.com/kcp-dev/apimachinery/pkg/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportVirtualWorkspace(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Need to Create a Producer w/ APIExport
	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	cfg := server.BaseConfig(t)

	kcpClients, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewClusterDynamicClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName, []string{"user-1", "user-2"}, nil, []string{"access"})
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, serviceProviderWorkspace, []string{"user-1", "user-2"}, nil, []string{"access"})

	setUpServiceProvider(ctx, dynamicClusterClient, kcpClients, serviceProviderWorkspace, cfg, t)

	bindConsumerToProvider(ctx, consumerWorkspace, serviceProviderWorkspace, t, kcpClients, cfg)

	createCowboyInConsumer(ctx, t, consumerWorkspace, wildwestClusterClient)

	clusterWorkspaceShardVirtualWorkspaceURLs := sets.NewString()
	t.Logf("Getting a list of VirtualWorkspaceURLs assigned to ClusterWorkspaceShards")
	require.Eventually(t, func() bool {
		clusterWorkspaceShards, err := kcpClients.TenancyV1alpha1().ClusterWorkspaceShards().List(logicalcluster.WithCluster(ctx, tenancyv1alpha1.RootCluster), metav1.ListOptions{})
		if err != nil {
			t.Logf("unexpected error while listing clusterworkspaceshards, err %v", err)
			return false
		}
		for _, s := range clusterWorkspaceShards.Items {
			if len(s.Spec.VirtualWorkspaceURL) == 0 {
				t.Logf("%q shard hasn't had assigned a virtual workspace URL", s.Name)
				return false
			}
			clusterWorkspaceShardVirtualWorkspaceURLs.Insert(s.Spec.VirtualWorkspaceURL)
		}
		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected all ClusterWorkspaceShards to have a VirtualWorkspaceURL assigned")

	t.Logf("test that the admin user can use the virtual workspace to get cowboys")
	apiExport, err := kcpClients.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport")
	require.Len(t, apiExport.Status.VirtualWorkspaces, clusterWorkspaceShardVirtualWorkspaceURLs.Len(), "unexpected virtual workspace URLs: %#v", apiExport.Status.VirtualWorkspaces)

	apiExportVWCfg := rest.CopyConfig(cfg)
	apiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	// Attempt to use VW using user-1 should expect an error
	t.Logf("Make sure that user-1 is denied")
	user1VWCfg := framework.UserConfig("user-1", apiExportVWCfg)
	wwUser1VC, err := wildwestclientset.NewForConfig(user1VWCfg)
	require.NoError(t, err)
	_, err = wwUser1VC.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
	require.True(t, apierrors.IsForbidden(err))

	// Create clusterRoleBindings for content access.
	t.Logf("create the cluster role and bindings to give access to the virtual workspace for user-1")
	cr, crb := createClusterRoleAndBindings("user-1-vw", "user-1", "User", []string{"list", "get"})
	_, err = kubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), crb, metav1.CreateOptions{})
	require.NoError(t, err)

	// Get cowboys from the virtual workspace with user-1.
	t.Logf("Get Cowboys with user-1")
	require.Eventually(t, func() bool {
		cbs, err := wwUser1VC.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
		if err != nil {
			return false
		}
		require.Equal(t, 1, len(cbs.Items))

		// Attempt to update it should fail
		cb := cbs.Items[0]
		cb.Status.Result = "updated"
		_, err = wwUser1VC.WildwestV1alpha1().Cowboys(cb.Namespace).UpdateStatus(logicalcluster.WithCluster(ctx, logicalcluster.From(&cb)), &cb, metav1.UpdateOptions{})
		require.Error(t, err)
		require.True(t, apierrors.IsForbidden(err))

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys from virtual workspace")

	// Test that users are able to update status of cowboys status
	t.Logf("create the cluster role and bindings to give access to the virtual workspace for user-2")
	cr, crb = createClusterRoleAndBindings("user-2-vw", "user-2", "User", []string{"update", "list"})
	_, err = kubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), crb, metav1.CreateOptions{})
	require.NoError(t, err)

	user2VWCfg := framework.UserConfig("user-2", apiExportVWCfg)
	wwUser2VC, err := wildwestclientset.NewClusterForConfig(user2VWCfg)
	require.NoError(t, err)
	t.Logf("Get Cowboy and update status with user-2")
	require.Eventually(t, func() bool {
		cbs, err := wwUser2VC.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		if len(cbs.Items) != 1 {
			return false
		}

		cb := cbs.Items[0]
		cb.Status.Result = "updated"
		_, err = wwUser2VC.Cluster(logicalcluster.From(&cb)).WildwestV1alpha1().Cowboys(cb.Namespace).UpdateStatus(ctx, &cb, metav1.UpdateOptions{})
		require.NoError(t, err)
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-2 to list cowboys from virtual workspace")

	// Create clusterRoleBindings for content write access.
	t.Logf("create the cluster role and bindings to give write access to the virtual workspace for user-1")
	cr, crb = createClusterRoleAndBindings("user-1-vw-write", "user-1", "User", []string{"create", "update", "delete", "deletecollection"})
	_, err = kubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), crb, metav1.CreateOptions{})
	require.NoError(t, err)

	// Test that user-1 is able to create, update, and delete cowboys
	var cowboy *wildwestv1alpha1.Cowboy
	require.Eventually(t, func() bool {
		t.Logf("create a cowboy with user-1 via APIExport virtual workspace server")
		cowboy = newCowboy("default", "cowboy-via-vw")
		var err error
		cowboy, err = wwUser1VC.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to create a cowboy via virtual workspace")

	t.Logf("update a cowboy with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "1"
	cowboy, err = wwUser1VC.WildwestV1alpha1().Cowboys("default").Update(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("make sure the updated cowboy has its generation incremented")
	require.Equal(t, cowboy.Generation, int64(2))

	t.Logf("update a cowboy status with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "2"
	cowboy.Status.Result = "test"
	_, err = wwUser1VC.WildwestV1alpha1().Cowboys("default").UpdateStatus(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy status update hasn't incremented the generation nor updated the spec")
	cowboy, err = wwUser1VC.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, consumerWorkspace), "cowboy-via-vw", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, cowboy.Generation, int64(2))
	require.Equal(t, cowboy.Spec.Intent, "1")
	require.Equal(t, cowboy.Status.Result, "test")

	t.Logf("delete a cowboy with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.WildwestV1alpha1().Cowboys("default").Delete(logicalcluster.WithCluster(ctx, consumerWorkspace), "cowboy-via-vw", metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy deleted with user-1 via APIExport virtual workspace server is gone")
	cowboys, err := wwUser1VC.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboys.Items))

	t.Logf("delete all cowboys with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.WildwestV1alpha1().Cowboys("default").DeleteCollection(logicalcluster.WithCluster(ctx, consumerWorkspace), metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	t.Logf("make sure all cowboys have been deleted")
	cowboys, err = wwUser1VC.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, len(cowboys.Items))
}

func TestAPIExportPermissionClaims(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Need to Create a Producer w/ APIExport
	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("provider"))
	serviceProviderSheriffs := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("provider-sheriffs"))
	serviceProviderSheriffsNotUsed := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("provider-sheriffs-unused"))
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer1"))
	consumerWorkspace2 := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer2"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewClusterDynamicClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSheriffs, kcpClusterClient, "wild.wild.west", "board the wanderer")
	// Creating extra export, to make sure the correct one is always used.
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSheriffsNotUsed, kcpClusterClient, "wild.wild.west", "use the giant spider")

	t.Logf("get the sheriffs api export's generated identity hash")
	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := kcpClusterClient.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderSheriffs), "wild.wild.west", metav1.GetOptions{})
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
		return false, "not done waiting for APIExportIdentiy to be marked valid, no condition exists"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be valid with identity hash")

	t.Logf("Found identity hash: %v", identityHash)

	t.Logf("adding sheriff before to make sure it will be labeled")
	apifixtures.BindToExport(ctx, t, serviceProviderSheriffs, "wild.wild.west", consumerWorkspace, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerWorkspace, "wild.wild.west", "in-vw-before")

	// Bind sheriffs into consumerWorkspace 2 and create a sheriff. we would expect this to not be retrieved
	t.Logf("bind and create a Sheriff in: %v to prove that it would not be retrieved", consumerWorkspace2)
	apifixtures.BindToExport(ctx, t, serviceProviderSheriffs, "wild.wild.west", consumerWorkspace2, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerWorkspace2, "wild.wild.west", "not-in-vw")

	t.Logf("create cowyboys API Export in %v with permission claims to core resources and sheriff provided by %v", serviceProviderWorkspace, serviceProviderSheriffs)
	setUpServiceProviderWithPermissionClaims(ctx, dynamicClusterClient, kcpClusterClient, serviceProviderWorkspace, cfg, identityHash, t)

	t.Logf("bind cowboys from %v to %v", serviceProviderWorkspace, consumerWorkspace)
	bindConsumerToProviderWithPermissionClaims(ctx, consumerWorkspace, serviceProviderWorkspace, t, kcpClusterClient, cfg, identityHash)

	framework.Eventually(t, func() (success bool, reason string) {
		// Get the binding and make sure that observed permission claims are all set.
		binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		for _, claim := range binding.Status.AppliedPermissionClaims {
			if claim.IdentityHash == identityHash {
				return true, "found observed accepted claim for identity"
			}
		}
		return false, "unable to find observed accepted claim"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to find observed accepted permission claim for identityHash")

	t.Logf("create cowboy in %v", consumerWorkspace)
	createCowboyInConsumer(ctx, t, consumerWorkspace, wildwestClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerWorkspace, "wild.wild.west", "in-vw")

	t.Logf("get virtual workspace client")
	var apiExport *apisv1alpha1.APIExport

	framework.Eventually(t, func() (bool, string) {
		var err error
		apiExport, err = kcpClusterClient.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		if len(apiExport.Status.VirtualWorkspaces) > 0 {
			return true, ""
		}
		return false, "waiting on virtual workspace to be ready"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	apiExportVWCfg := rest.CopyConfig(cfg)
	apiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	wildwestVCClients, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Logf("verify that the export resource is retrievable")
	cowboysProjected, err := wildwestVCClients.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	t.Logf("Ensuring the appropriate core/v1 resources are available")
	dynamicVWClusterClient, err := kcpdynamic.NewClusterDynamicClientForConfig(apiExportVWCfg)
	require.NoError(t, err, "error creating dynamic cluster client for %q", apiExportVWCfg.Host)

	grantedGVRs := []schema.GroupVersionResource{
		{Version: "v1", Resource: "configmaps"},
		{Version: "v1", Resource: "secrets"},
		{Version: "v1", Resource: "serviceaccounts"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
	}

	for _, gvr := range grantedGVRs {
		t.Logf("Trying to wildcard list %q", gvr)
		list, err := dynamicVWClusterClient.Cluster(logicalcluster.Wildcard).Resource(gvr).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing %q", gvr)
		for _, u := range list.Items {
			t.Logf("gvr %q, %s|%s/%s", gvr, logicalcluster.From(&u), u.GetNamespace(), u.GetName())
			require.Equal(t, consumerWorkspace, logicalcluster.From(&u))
		}
	}

	t.Logf("Verify that two sherrifs are eventually returned")
	var ul *unstructured.UnstructuredList
	framework.Eventually(t, func() (done bool, str string) {
		gvr := schema.GroupVersionResource{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"}
		var err error
		ul, err = dynamicVWClusterClient.Cluster(logicalcluster.Wildcard).Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(ul.Items) == 2 {
			return true, "found two items"
		}
		return false, fmt.Sprintf("waiting on dynamic client to find both objects, found %#v objects", ul.Items)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to wait for the two objects to be returned")

	require.Empty(t, cmp.Diff(
		[]string{ul.Items[0].GetName(), ul.Items[1].GetName()},
		[]string{"in-vw", "in-vw-before"},
	))
}

func TestAPIExportInternalAPIsDrift(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	cfg := server.BaseConfig(t)
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	require.NoError(t, err, "failed to construct discovery client for server")

	orgClusterName := framework.NewOrganizationFixture(t, server)
	anyWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	apis, err := gatherInternalAPIs(discoveryClient.WithCluster(anyWorkspace), t)
	require.NoError(t, err, "failed to gather built-in apis for server")

	require.Equal(t, len(apis), len(apireconciler.InternalAPIs))

	require.ElementsMatch(t, apis, apireconciler.InternalAPIs)
}

func gatherInternalAPIs(discoveryClient discovery.DiscoveryInterface, t *testing.T) ([]internalapis.InternalAPI, error) {
	_, apiResourcesLists, err := discoveryClient.ServerGroupsAndResources()
	if err != nil {
		return nil, err
	}
	t.Logf("gathering internal apis, found %d", len(apiResourcesLists))

	apisByGVK := map[schema.GroupVersionKind]internalapis.InternalAPI{}

	for _, apiResourcesList := range apiResourcesLists {
		gv, err := schema.ParseGroupVersion(apiResourcesList.GroupVersion)
		if err != nil {
			t.Errorf("error parsing group version %v, err: %v", apiResourcesList.GroupVersion, err)
			continue
		}
		// ignore kcp resources
		if strings.HasSuffix(gv.Group, ".kcp.dev") {
			continue
		}
		// ignore authn/authz non-crud apis
		if gv.Group == "authentication.k8s.io" || gv.Group == "authorization.k8s.io" {
			continue
		}
		for _, apiResource := range apiResourcesList.APIResources {
			gvk := schema.GroupVersionKind{Kind: apiResource.Kind, Version: gv.Version, Group: gv.Group}

			hasStatus := false
			if strings.HasSuffix(apiResource.Name, "/status") {
				if api, ok := apisByGVK[gvk]; ok {
					api.HasStatus = true
					apisByGVK[gvk] = api
					continue
				}
				hasStatus = true
			} else if strings.Contains(apiResource.Name, "/") {
				// ignore other subresources
				continue
			}
			resourceScope := apiextensionsv1.ClusterScoped
			if apiResource.Namespaced {
				resourceScope = apiextensionsv1.NamespaceScoped
			}

			instance, err := genericcontrolplanescheme.Scheme.New(gvk)
			if err != nil {
				if extensionsapiserver.Scheme.Recognizes(gvk) {
					// Not currently supporting permission claim for CustomResourceDefinition.
					// extensionsapiserver.Scheme is recursive and fails in internalapis.CreateAPIResourceSchemas in schema converter
					t.Logf("permission claims not currently supported for gvk: %v/%v %v", gv.Group, gv.Version, apiResource.Kind)
				} else {
					t.Errorf("error creating instance for gvk: %v/%v %v err: %v", gv.Group, gv.Version, apiResource.Kind, err)
				}
				continue
			}

			t.Logf("Adding internal API %v/%v %v , has status: %t", gv.Group, gv.Version, apiResource.Kind, hasStatus)

			if apiResource.SingularName == "" {
				apiResource.SingularName = strings.ToLower(apiResource.Kind)
			}

			apisByGVK[gvk] = internalapis.InternalAPI{
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   apiResource.Name,
					Singular: apiResource.SingularName,
					Kind:     apiResource.Kind,
				},
				GroupVersion:  gv,
				Instance:      instance,
				ResourceScope: resourceScope,
				HasStatus:     hasStatus,
			}
		}

	}
	internalAPIs := make([]internalapis.InternalAPI, 0, len(apisByGVK))
	for _, api := range apisByGVK {
		internalAPIs = append(internalAPIs, api)
	}
	return internalAPIs, nil
}

func setUpServiceProviderWithPermissionClaims(ctx context.Context, dynamicClusterClient *kcpdynamic.ClusterDynamicClient, kcpClients clientset.Interface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, identityHash string, t *testing.T) {
	claims := []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "serviceaccounts"},
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			IdentityHash:  identityHash,
		},
	}
	setUpServiceProvider(ctx, dynamicClusterClient, kcpClients, serviceProviderWorkspace, cfg, t, claims...)
}

func setUpServiceProvider(ctx context.Context, dynamicClusterClient *kcpdynamic.ClusterDynamicClient, kcpClients clientset.Interface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, t *testing.T, claims ...apisv1alpha1.PermissionClaim) {

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	clusterCfg := kcpclienthelper.ConfigWithCluster(cfg, serviceProviderWorkspace)
	serviceProviderClient, err := clientset.NewForConfig(clusterCfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims:      claims,
		},
	}
	_, err = kcpClients.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func bindConsumerToProviderWithPermissionClaims(ctx context.Context, consumerWorkspace, providerWorkspace logicalcluster.Name, t *testing.T, kcpClients clientset.Interface, cfg *rest.Config, identityHash string) {
	claims := []apisv1alpha1.AcceptablePermissionClaim{
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "serviceaccounts"},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
				IdentityHash:  identityHash,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
	}

	bindConsumerToProvider(ctx, consumerWorkspace, providerWorkspace, t, kcpClients, cfg, claims...)
}

func bindConsumerToProvider(ctx context.Context, consumerWorkspace, providerWorkspace logicalcluster.Name, t *testing.T, kcpClients clientset.Interface, cfg *rest.Config, claims ...apisv1alpha1.AcceptablePermissionClaim) {
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       providerWorkspace.String(),
					ExportName: "today-cowboys",
				},
			},
			PermissionClaims: claims,
		},
	}

	consumerWorkspaceCfg := kcpclienthelper.ConfigWithCluster(cfg, consumerWorkspace)
	consumerWsClient, err := clientset.NewForConfig(consumerWorkspaceCfg)
	require.NoError(t, err)

	_, err = kcpClients.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := consumerWsClient.Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
	resources, err := consumerWsClient.Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
}

func createCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Name, wildwestClusterClient wildwestclientset.Interface) {
	t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumer1Workspace)

	t.Logf("Make sure list shows nothing to start")
	cowboyClusterClient := wildwestClusterClient.WildwestV1alpha1().Cowboys("default")
	var cowboys *wildwestv1alpha1.CowboyList
	// Adding a poll here to wait for the user's to get access via RBAC informer updates.

	require.Eventually(t, func() bool {
		var err error
		cowboys, err = cowboyClusterClient.List(logicalcluster.WithCluster(ctx, consumer1Workspace), metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list ")
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside consumer workspace %q", consumer1Workspace)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumer1Workspace)
	cowboyName := fmt.Sprintf("cowboy-%s", consumer1Workspace.Base())
	cowboy := newCowboy("default", cowboyName)
	_, err := cowboyClusterClient.Create(logicalcluster.WithCluster(ctx, consumer1Workspace), cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumer1Workspace)
}

func newCowboy(namespace, name string) *wildwestv1alpha1.Cowboy {
	return &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func createClusterRoleAndBindings(name, subjectName, subjectKind string, verbs []string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     verbs,
				APIGroups: []string{apisv1alpha1.SchemeGroupVersion.Group},
				Resources: []string{"apiexports/content"},
			},
		},
	}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
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
	return clusterRole, clusterRoleBinding
}
