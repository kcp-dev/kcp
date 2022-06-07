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
	"testing"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
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

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName, []string{"user-1", "user-2"}, nil, []string{"member"})
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, serviceProviderWorkspace, []string{"user-1", "user-2"}, nil, []string{"member", "access"})

	setUpServiceProvider(ctx, dynamicClients, kcpClients, serviceProviderWorkspace, cfg, t)

	bindConsumerToProvider(ctx, consumerWorkspace, serviceProviderWorkspace, t, kcpClients, cfg)

	createCowboyInConsumer(ctx, t, consumerWorkspace, wildwestClusterClient)

	t.Logf("test that the admin user can use the virtual workspace to get cowboys")
	apiExport, err := kcpClients.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport")
	require.Len(t, apiExport.Status.VirtualWorkspaces, 1, "unexpected virtual workspace URLs: %#v", apiExport.Status.VirtualWorkspaces)

	apiExportVWCfg := rest.CopyConfig(cfg)
	apiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	wildwestVCClients, err := wildwestclientset.NewClusterForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClients.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	// Attempt to use VW using user-1 should expect an error
	t.Logf("Make sure that user-1 is denied")
	user1VWCfg := framework.UserConfig("user-1", apiExportVWCfg)
	wwUser1VC, err := wildwestclientset.NewClusterForConfig(user1VWCfg)
	require.NoError(t, err)
	_, err = wwUser1VC.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
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
		cbs, err := wwUser1VC.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}
		require.Equal(t, 1, len(cbs.Items))

		// Attempt to update it should fail
		cb := cbs.Items[0]
		cb.Status.Result = "updated"
		_, err = wwUser1VC.Cluster(logicalcluster.From(&cb)).WildwestV1alpha1().Cowboys(cb.Namespace).UpdateStatus(ctx, &cb, metav1.UpdateOptions{})
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
		cowboy, err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to create a cowboy via virtual workspace")

	t.Logf("update a cowboy with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "1"
	cowboy, err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").Update(ctx, cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("make sure the updated cowboy has its generation incremented")
	require.Equal(t, cowboy.Generation, int64(2))

	t.Logf("update a cowboy status with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "2"
	cowboy.Status.Result = "test"
	_, err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").UpdateStatus(ctx, cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy status update hasn't incremented the generation nor updated the spec")
	cowboy, err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").Get(ctx, "cowboy-via-vw", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, cowboy.Generation, int64(2))
	require.Equal(t, cowboy.Spec.Intent, "1")
	require.Equal(t, cowboy.Status.Result, "test")

	t.Logf("delete a cowboy with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").Delete(ctx, "cowboy-via-vw", metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy deleted with user-1 via APIExport virtual workspace server is gone")
	cowboys, err := wwUser1VC.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboys.Items))

	t.Logf("delete all cowboys with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	t.Logf("make sure all cowboys have been deleted")
	cowboys, err = wwUser1VC.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, len(cowboys.Items))
}

func TestAPIExportWithPermissionClaims(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Need to Create a Producer w/ APIExport
	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	serviceProviderSherriffs := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	kcpClientOld, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSherriffs, kcpClientOld, "wild.wild.west", "board the wanderer")

	t.Logf("get the sheriffs api export's generated identity hash")
	identityHash := ""
	wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		sheriffExport, err := kcpClients.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderSherriffs), "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		if conditions.IsTrue(sheriffExport, apisv1alpha1.APIExportIdentityValid) {
			identityHash = sheriffExport.Status.IdentityHash
			return true, nil
		}
		return false, nil
	})

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, serviceProviderSherriffs, "wild.wild.west", consumerWorkspace, kcpClientOld)
	setUpServiceProviderWithPermissionClaims(ctx, dynamicClients, kcpClients, serviceProviderWorkspace, cfg, identityHash, t)

	bindConsumerToProvider(ctx, consumerWorkspace, serviceProviderWorkspace, t, kcpClients, cfg)

	createCowboyInConsumer(ctx, t, consumerWorkspace, wildwestClusterClient)
	t.Logf("test that the admin user can use the virtual workspace to get cowboys")
	apiExport, err := kcpClients.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport")
	require.Len(t, apiExport.Status.VirtualWorkspaces, 1, "unexpected virtual workspace URLs: %#v", apiExport.Status.VirtualWorkspaces)

	apiExportVWCfg := rest.CopyConfig(cfg)
	apiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	wildwestVCClients, err := wildwestclientset.NewClusterForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClients.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	t.Logf("Ensuring the appropriate core/v1 resources are available")
	dynamicVWClients, err := dynamic.NewClusterForConfig(apiExportVWCfg)
	require.NoError(t, err, "error creating dynamic cluster client for %q", apiExportVWCfg.Host)

	grantedGVRs := []schema.GroupVersionResource{
		{Version: "v1", Resource: "namespaces"},
		{Version: "v1", Resource: "configmaps"},
		{Version: "v1", Resource: "secrets"},
		{Version: "v1", Resource: "serviceaccounts"},
		{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"},
	}

	for _, gvr := range grantedGVRs {
		t.Logf("Trying to wildcard list %q", gvr)
		_, err = dynamicVWClients.Cluster(logicalcluster.Wildcard).Resource(gvr).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing %q", gvr)
	}

}

func setUpServiceProviderWithPermissionClaims(ctx context.Context, dynamicClients *dynamic.Cluster, kcpClients clientset.Interface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, identityHash string, t *testing.T) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	clusterCfg := kcpclienthelper.ConfigWithCluster(cfg, serviceProviderWorkspace)
	serviceProviderClient, err := clientset.NewForConfig(clusterCfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(serviceProviderWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: metav1.GroupResource{Group: "", Resource: "namespaces"},
				},
				{
					GroupResource: metav1.GroupResource{Group: "", Resource: "configmaps"},
				},
				{
					GroupResource: metav1.GroupResource{Group: "", Resource: "secrets"},
				},
				{
					GroupResource: metav1.GroupResource{Group: "", Resource: "serviceaccounts"},
				},
				{
					GroupResource: metav1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
					IdentityHash:  identityHash,
				},
			},
		},
	}
	_, err = kcpClients.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func setUpServiceProvider(ctx context.Context, dynamicClients *dynamic.Cluster, kcpClients clientset.Interface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, t *testing.T) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	clusterCfg := kcpclienthelper.ConfigWithCluster(cfg, serviceProviderWorkspace)
	serviceProviderClient, err := clientset.NewForConfig(clusterCfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(serviceProviderWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func bindConsumerToProvider(ctx context.Context, consumerWorkspace, providerWorkspace logicalcluster.Name, t *testing.T, kcpClients clientset.Interface, cfg *rest.Config) {
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

func createCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Name, wildwestClusterClient *wildwestclientset.Cluster) {
	t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumer1Workspace)

	t.Logf("Make sure list shows nothing to start")
	cowboyClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	var cowboys *wildwestv1alpha1.CowboyList
	// Adding a poll here to wait for the user's to get access via RBAC informer updates.

	require.Eventually(t, func() bool {
		var err error
		cowboys, err = cowboyClient.List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list ")
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside consumer workspace %q", consumer1Workspace)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumer1Workspace)
	cowboyName := fmt.Sprintf("cowboy-%s", consumer1Workspace.Base())
	cowboy := newCowboy("default", cowboyName)
	_, err := cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
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
