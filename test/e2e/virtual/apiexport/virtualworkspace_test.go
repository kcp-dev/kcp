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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apiexportbuiltin "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Need to Create a Producer w/ APIExport
	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	consumerClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName.Path(), []string{"user-1", "user-2", "user-3"}, nil, false)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, serviceProviderClusterName.Path(), []string{"user-1", "user-2"}, nil, false)
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, consumerClusterName.Path(), []string{"user-3"}, nil, true)

	cr, crb := createClusterRoleAndBindings("user-3-binding", "user-3", "User", []string{"bind"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports", "today-cowboys")
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	setUpServiceProvider(ctx, dynamicClusterClient, kcpClients, serviceProviderClusterName.Path(), cfg, t)

	t.Logf("test that the virtualWorkspaceURL is not set on initial APIExport creation")
	apiExport, err := kcpClients.Cluster(serviceProviderClusterName.Path()).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.Empty(t, apiExport.Status.VirtualWorkspaces)
	require.NoError(t, err, "error getting APIExport")

	// create API bindings in consumerWorkspace as user-3 with only bind permissions in serviceProviderWorkspace but not general access.
	user3KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err, "failed to construct client for user-3")
	bindConsumerToProvider(ctx, consumerClusterName.Path(), serviceProviderClusterName, t, user3KcpClient, cfg)
	createCowboyInConsumer(ctx, t, consumerClusterName.Path(), wildwestClusterClient)

	clusterWorkspaceShardVirtualWorkspaceURLs := sets.NewString()
	t.Logf("Getting a list of VirtualWorkspaceURLs assigned to ClusterWorkspaceShards")
	require.Eventually(t, func() bool {
		clusterWorkspaceShards, err := kcpClients.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaceShards().List(ctx, metav1.ListOptions{})
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
	apiExport, err = kcpClients.Cluster(serviceProviderClusterName.Path()).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
	require.NoError(t, err, "error getting APIExport")
	require.Len(t, apiExport.Status.VirtualWorkspaces, clusterWorkspaceShardVirtualWorkspaceURLs.Len(), "unexpected virtual workspace URLs: %#v", apiExport.Status.VirtualWorkspaces)

	apiExportVWCfg := rest.CopyConfig(cfg)
	apiExportVWCfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	discoveryVCClusterClient, err := kcpdiscovery.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	t.Logf("test that the virtual workspace includes APIBindings")
	resources, err := discoveryVCClusterClient.ServerResourcesForGroupVersion(apisv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving APIExport discovery")
	require.True(t, resourceExists(resources, "apibindings"), "missing apibindings")

	// Attempt to use VW using user-1 should expect an error
	t.Logf("Make sure that user-1 is denied")
	user1VWCfg := framework.UserConfig("user-1", apiExportVWCfg)
	wwUser1VC, err := wildwestclientset.NewForConfig(user1VWCfg)
	require.NoError(t, err)
	_, err = wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.True(t, apierrors.IsForbidden(err))

	// Create clusterRoleBindings for content access.
	t.Logf("create the cluster role and bindings to give access to the virtual workspace for user-1")
	cr, crb = createClusterRoleAndBindings("user-1-vw", "user-1", "User", []string{"list", "get"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	// Get cowboys from the virtual workspace with user-1.
	t.Logf("Get Cowboys with user-1")
	require.Eventually(t, func() bool {
		cbs, err := wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
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
	cr, crb = createClusterRoleAndBindings("user-2-vw", "user-2", "User", []string{"update", "list"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	user2VWCfg := framework.UserConfig("user-2", apiExportVWCfg)
	wwUser2VC, err := wildwestclientset.NewForConfig(user2VWCfg)
	require.NoError(t, err)
	t.Logf("Get Cowboy and update status with user-2")
	var testCowboy wildwestv1alpha1.Cowboy
	require.Eventually(t, func() bool {
		cbs, err := wwUser2VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
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
		testCowboy = cb
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-2 to list cowboys from virtual workspace")

	// Create clusterRoleBindings for content write access.
	t.Logf("create the cluster role and bindings to give write access to the virtual workspace for user-1")
	cr, crb = createClusterRoleAndBindings("user-1-vw-write", "user-1", "User", []string{"create", "update", "patch", "delete", "deletecollection"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	// Test that user-1 is able to create, update, and delete cowboys
	var cowboy *wildwestv1alpha1.Cowboy
	require.Eventually(t, func() bool {
		t.Logf("create a cowboy with user-1 via APIExport virtual workspace server")
		cowboy = newCowboy("default", "cowboy-via-vw")
		var err error
		cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to create a cowboy via virtual workspace")

	t.Logf("update a cowboy with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "1"
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Update(ctx, cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("make sure the updated cowboy has its generation incremented")
	require.Equal(t, cowboy.Generation, int64(2))

	t.Logf("update a cowboy status with user-1 via APIExport virtual workspace server")
	cowboy.Spec.Intent = "2"
	cowboy.Status.Result = "test"
	_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").UpdateStatus(ctx, cowboy, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy status update hasn't incremented the generation nor updated the spec")
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "cowboy-via-vw", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, cowboy.Generation, int64(2))
	require.Equal(t, cowboy.Spec.Intent, "1")
	require.Equal(t, cowboy.Status.Result, "test")

	t.Logf("patch (application/merge-patch+json) a cowboy with user-1 via APIExport virtual workspace server")
	patchedCowboy := cowboy.DeepCopy()
	patchedCowboy.Spec.Intent = "3"
	source := encodeJSON(t, cowboy)
	target := encodeJSON(t, patchedCowboy)
	mergePatch, err := jsonpatch.CreateMergePatch(source, target)
	require.NoError(t, err)
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
		Patch(ctx, "cowboy-via-vw", types.MergePatchType, mergePatch, metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("patch (application/apply-patch+yaml) a cowboy with user-1 via APIExport virtual workspace server")
	applyCowboy := newCowboy("default", "cowboy-via-vw")
	applyCowboy.Spec.Intent = "4"
	applyPatch := encodeJSON(t, applyCowboy)
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
		Patch(ctx, "cowboy-via-vw", types.ApplyPatchType, applyPatch, metav1.PatchOptions{FieldManager: "e2e-test-runner", Force: pointer.Bool(true)})
	require.NoError(t, err)

	t.Logf("patching a non-existent cowboy with user-1 via APIExport virtual workspace server should fail")
	patchedCowboy = cowboy.DeepCopy()
	patchedCowboy.Spec.Intent = "1"
	source = encodeJSON(t, cowboy)
	target = encodeJSON(t, patchedCowboy)
	mergePatch, err = jsonpatch.CreateMergePatch(source, target)
	require.NoError(t, err)
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
		Patch(ctx, "cowboy-via-vw-merge-patch", types.MergePatchType, mergePatch, metav1.PatchOptions{})
	require.EqualError(t, err, "cowboys.wildwest.dev \"cowboy-via-vw-merge-patch\" not found")

	t.Logf("create a cowboy with user-1 via APIExport virtual workspace server using Server-Side Apply")
	cowboySSA := newCowboy("default", "cowboy-via-vw-ssa")
	cowboySSA.Spec.Intent = "1"
	applyPatch = encodeJSON(t, cowboySSA)
	cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
		Patch(ctx, "cowboy-via-vw-ssa", types.ApplyPatchType, applyPatch, metav1.PatchOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err)

	t.Logf("delete a cowboy with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Delete(ctx, "cowboy-via-vw", metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("make sure the cowboy deleted with user-1 via APIExport virtual workspace server is gone")
	cowboys, err := wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, len(cowboys.Items))
	var names []string
	for _, c := range cowboys.Items {
		names = append(names, c.Name)
	}
	require.ElementsMatch(t, []string{testCowboy.Name, "cowboy-via-vw-ssa"}, names)

	t.Logf("delete all cowboys with user-1 via APIExport virtual workspace server")
	err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err)

	t.Logf("make sure all cowboys have been deleted")
	cowboys, err = wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, len(cowboys.Items))
}

func TestAPIExportAPIBindingsAccess(t *testing.T) {
	t.Skip("https://github.com/kcp-dev/kcp/issues/2263")
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	clusterName1 := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("workspace1"))
	clusterName2 := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("workspace2"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	discoveryClusterClient, err := kcpdiscovery.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct discovery cluster client for server")

	mappers := make(map[logicalcluster.Name]meta.RESTMapper)
	mappers[clusterName1.Path()] = restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(kcpClusterClient.Cluster(clusterName1.Path()).Discovery()),
	)
	mappers[clusterName2.Path()] = restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(kcpClusterClient.Cluster(clusterName2.Path()).Discovery()),
	)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	create := func(clusterName logicalcluster.Name, msg, file string, transforms ...helpers.TransformFileFunc) {
		t.Helper()
		t.Logf("%s: creating %s", clusterName, msg)
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(clusterName), mappers[clusterName], nil, file, testFiles, transforms...)
		require.NoError(t, err, "%s: error creating %s", clusterName, msg)
	}

	create(clusterName1.Path(), "APIResourceSchema 1", "apibindings_access_schema1.yaml")
	create(clusterName1.Path(), "APIExport 1", "apibindings_access_export1.yaml")
	create(clusterName1.Path(), "APIResourceSchema 2", "apibindings_access_schema2.yaml")
	create(clusterName1.Path(), "APIExport 2", "apibindings_access_export2.yaml")
	create(clusterName1.Path(), "APIBinding referencing APIExport 1", "apibindings_access_binding1.yaml")
	create(clusterName1.Path(), "APIBinding referencing APIExport 2", "apibindings_access_binding2.yaml")

	create(clusterName2.Path(), "APIBinding referencing APIExport 1", "apibindings_access_binding1.yaml", func(bs []byte) ([]byte, error) {
		var binding apisv1alpha1.APIBinding
		err := yaml.Unmarshal(bs, &binding)
		require.NoError(t, err, "error unmarshaling binding")
		binding.Spec.Reference.Export.Cluster = clusterName1
		out, err := yaml.Marshal(&binding)
		require.NoError(t, err, "error marshaling binding")
		return out, nil
	})
	create(clusterName2.Path(), "APIResourceSchema 3", "apibindings_access_schema3.yaml")
	create(clusterName2.Path(), "APIExport 3", "apibindings_access_export3.yaml")
	create(clusterName2.Path(), "APIBinding referencing APIExport 3", "apibindings_access_binding3.yaml")

	exportURLs := make(map[string]string)
	getExportURL := func(clusterName logicalcluster.Name, exportName string) string {
		var url string
		framework.Eventually(t, func() (bool, string) {
			apiExport, err := kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
			require.NoError(t, err, "error getting APIExport %s|%s", clusterName, exportName)

			if len(apiExport.Status.VirtualWorkspaces) == 0 {
				return false, fmt.Sprintf("%#v", apiExport.Status)
			}

			url = apiExport.Status.VirtualWorkspaces[0].URL
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "did not get a virtual workspace URL for APIExport %s", exportName)
		return url
	}

	exportURLs["export1"] = getExportURL(clusterName1.Path(), "export1")
	exportURLs["export2"] = getExportURL(clusterName1.Path(), "export2")
	exportURLs["export3"] = getExportURL(clusterName2.Path(), "export3")

	verifyDiscovery := func(clusterName logicalcluster.Name, exportName string) {
		t.Helper()

		t.Logf("Verifying APIExport %s|%s discovery has apis.kcp.dev", clusterName, exportName)
		discoveryClient := discoveryClusterClient.Cluster(clusterName)
		framework.Eventually(t, func() (bool, string) {
			groups, err := discoveryClient.ServerGroups()
			require.NoError(t, err, "error getting discovery server groups for %s|%s", clusterName, exportName)

			return groupExists(groups, apis.GroupName), fmt.Sprintf("%#v", groups)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "never saw apis.kcp.dev group for export %s|%s", clusterName, exportName)

		t.Logf("Verifying APIExport %s discovery has apibindings", exportName)
		framework.Eventually(t, func() (bool, string) {
			resources, err := discoveryClient.ServerResourcesForGroupVersion(apisv1alpha1.SchemeGroupVersion.String())
			require.NoError(t, err, "error getting discovery server resources for apis.kcp.dev for %s|%s", clusterName, exportName)

			return resourceExists(resources, "apibindings"), fmt.Sprintf("%#v", resources)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "never saw apis.kcp.dev group for export %s|%s", clusterName, exportName)
	}

	verifyDiscovery(clusterName1.Path(), "export1")
	verifyDiscovery(clusterName1.Path(), "export2")
	verifyDiscovery(clusterName2.Path(), "export3")

	verifyBindings := func(clusterName logicalcluster.Name, exportName string, isValid func(bindings []apisv1alpha1.APIBinding) error) {
		t.Helper()

		apiExportCfg := rest.CopyConfig(cfg)
		apiExportCfg.Host = exportURLs[exportName] + "/clusters/*"

		apiExportKCPClient, err := kcpclientset.NewForConfig(apiExportCfg)
		require.NoError(t, err, "error creating wildcard kcp client for %s|%s", clusterName, exportName)

		framework.Eventually(t, func() (bool, string) {
			bindings, err := apiExportKCPClient.ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing bindings for %s|%s", clusterName, exportName)

			if err := isValid(bindings.Items); err != nil {
				return false, err.Error()
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "did not see expected bindings for %s|%s", clusterName, exportName)

	}
	t.Logf("Verifying APIExport 1 only serves APIBinding 1|1 and 2|1")
	verifyBindings(clusterName1.Path(), "export1", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := tenancy.From(&b)
			if clusterName == clusterName1 && b.Name == "binding1" {
				continue
			}
			if clusterName == clusterName2 && b.Name == "binding1" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Verifying APIExport 2 only serves APIBinding 2")
	verifyBindings(clusterName1.Path(), "export2", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := tenancy.From(&b)
			if clusterName == clusterName1 && b.Name == "binding2" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Updating APIExport 1 to claim APIBindings")
	export1, err := kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIExports().Get(ctx, "export1", metav1.GetOptions{})
	require.NoError(t, err)
	export1.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    "apis.kcp.dev",
				Resource: "apibindings",
			},
			All: true,
		},
	}
	_, err = kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIExports().Update(ctx, export1, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Updating APIBinding 1 to accept APIBindings claim")
	binding1, err := kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIBindings().Get(ctx, "binding1", metav1.GetOptions{})
	require.NoError(t, err)
	oldData := encodeJSON(t, apisv1alpha1.APIBinding{})
	newData := encodeJSON(t, apisv1alpha1.APIBinding{
		Spec: apisv1alpha1.APIBindingSpec{
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: export1.Spec.PermissionClaims[0],
					State:           apisv1alpha1.ClaimAccepted,
				},
			},
		},
	})
	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	require.NoError(t, err, "error creating patch")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIBindings().Patch(ctx, binding1.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			return true, ""
		}
		require.False(t, apierrors.IsConflict(err))
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating %s|%s to accept claims", clusterName1, binding1.Name)

	t.Logf("Verifying APIExport 1 serves APIBindings 1|1, 1|2, and 2|1")
	verifyBindings(clusterName1.Path(), "export1", func(bindings []apisv1alpha1.APIBinding) error {
		// "workspace1|binding1", "workspace2|binding1", "workspace1|binding2")
		actualWorkspace1 := sets.NewString()

		for _, b := range bindings {
			clusterName := tenancy.From(&b)
			if clusterName != clusterName1 {
				if clusterName == clusterName2 && b.Name == "binding1" {
					continue
				}

				return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
			}

			actualWorkspace1.Insert(b.Name)
		}

		expectedWorkspace1 := sets.NewString("binding1", "binding2")
		if !actualWorkspace1.IsSuperset(expectedWorkspace1) {
			return fmt.Errorf("mismatch %s", cmp.Diff(expectedWorkspace1, actualWorkspace1))
		}

		return nil
	})

	t.Logf("Verifying APIExport 2 still only serves APIBinding 2")
	verifyBindings(clusterName1.Path(), "export2", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := tenancy.From(&b)
			if clusterName == clusterName1 && b.Name == "binding2" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Updating APIBinding 1 to reject APIBindings claim")
	binding1, err = kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIBindings().Get(ctx, "binding1", metav1.GetOptions{})
	require.NoError(t, err)
	oldData = encodeJSON(t, binding1)
	binding1.Spec.PermissionClaims = nil
	newData = encodeJSON(t, binding1)
	patchBytes, err = jsonpatch.CreateMergePatch(oldData, newData)
	require.NoError(t, err, "error creating patch")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(clusterName1.Path()).ApisV1alpha1().APIBindings().Patch(ctx, binding1.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			return true, ""
		}
		require.False(t, apierrors.IsConflict(err))
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating %s|%s to no longer accept claims", clusterName1, binding1.Name)

	t.Logf("Verifying APIExport 1 back to only serving its own bindings")
	verifyBindings(clusterName1.Path(), "export1", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := tenancy.From(&b)
			if clusterName == clusterName1 && b.Name == "binding1" {
				continue
			}
			if clusterName == clusterName2 && b.Name == "binding1" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})
}

func TestAPIExportPermissionClaims(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Need to Create a Producer w/ APIExport
	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("provider"))
	serviceProviderSheriffs := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("provider-sheriffs"))
	serviceProviderSheriffsNotUsed := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("provider-sheriffs-unused"))
	consumerClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("consumer1"))
	consumerClusterName2 := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("consumer2"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSheriffs.Path(), kcpClusterClient, "wild.wild.west", "board the wanderer")
	// Creating extra export, to make sure the correct one is always used.
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSheriffsNotUsed.Path(), kcpClusterClient, "wild.wild.west", "use the giant spider")

	t.Logf("get the sheriffs apiexport's generated identity hash")
	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := kcpClusterClient.Cluster(serviceProviderSheriffs.Path()).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
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

	t.Logf("adding sheriff before to make sure it will be labeled")
	apifixtures.BindToExport(ctx, t, serviceProviderSheriffs, "wild.wild.west", consumerClusterName.Path(), kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerClusterName.Path(), "wild.wild.west", "in-vw-before")

	// Bind sheriffs into consumerWorkspace 2 and create a sheriff. we would expect this to not be retrieved
	t.Logf("bind and create a Sheriff in: %v to prove that it would not be retrieved", consumerClusterName2)
	apifixtures.BindToExport(ctx, t, serviceProviderSheriffs, "wild.wild.west", consumerClusterName2.Path(), kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerClusterName2.Path(), "wild.wild.west", "not-in-vw")

	t.Logf("create cowyboys API Export in %v with permission claims to core resources and sheriff provided by %v", serviceProviderClusterName, serviceProviderSheriffs)
	setUpServiceProviderWithPermissionClaims(ctx, dynamicClusterClient, kcpClusterClient, serviceProviderClusterName.Path(), cfg, identityHash, t)

	t.Logf("bind cowboys from %v to %v", serviceProviderClusterName, consumerClusterName)
	bindConsumerToProvider(ctx, consumerClusterName.Path(), serviceProviderClusterName, t, kcpClusterClient, cfg)

	t.Logf("create cowboy in %v", consumerClusterName)
	createCowboyInConsumer(ctx, t, consumerClusterName.Path(), wildwestClusterClient)

	t.Logf("get virtual workspace client")
	var apiExport *apisv1alpha1.APIExport

	framework.Eventually(t, func() (bool, string) {
		var err error
		apiExport, err = kcpClusterClient.Cluster(serviceProviderClusterName.Path()).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
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

	t.Logf("verify that the exported resource is retrievable")
	cowboysProjected, err := wildwestVCClients.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	dynamicVWClusterClient, err := kcpdynamic.NewForConfig(apiExportVWCfg)
	require.NoError(t, err, "error creating dynamic cluster client for %q", apiExportVWCfg.Host)

	sheriffsGVR := schema.GroupVersionResource{Version: "v1", Resource: "sheriffs", Group: "wild.wild.west"}

	claimedGVRs := []schema.GroupVersionResource{
		{Version: "v1", Resource: "configmaps"},
		{Version: "v1", Resource: "secrets"},
		{Version: "v1", Resource: "serviceaccounts"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
		sheriffsGVR,
		apisv1alpha1.SchemeGroupVersion.WithResource("apibindings"),
	}

	t.Logf("verify that we get empty lists for all claimed resources (other than apibindings) because the claims have not been accepted yet")
	for _, gvr := range claimedGVRs {
		t.Logf("Trying to wildcard list %q", gvr)
		list, err := dynamicVWClusterClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing %q", gvr)
		if gvr == apisv1alpha1.SchemeGroupVersion.WithResource("apibindings") {
			// for this one we always see the reflexive objects
			require.Equal(t, 1, len(list.Items), "expected to find 1 apibinding, got %#v", list.Items)
			for _, binding := range list.Items {
				require.Equal(t, "cowboys", binding.GetName(), "expected binding name to be \"cowboys\"")
			}
		} else {
			require.Empty(t, list.Items, "expected 0 items but got %#v for gvr %s", list.Items, gvr)
		}
	}

	t.Logf("retrieving cowboys apibinding")
	apiBinding, err := kcpClusterClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	require.NoError(t, err, "error getting cowboys apibinding")

	t.Logf("patching apibinding to accept all permissionclaims")
	bindingWithClaims := apiBinding.DeepCopy()
	for i := range apiBinding.Status.ExportPermissionClaims {
		claim := apiBinding.Status.ExportPermissionClaims[i]
		bindingWithClaims.Spec.PermissionClaims = append(bindingWithClaims.Spec.PermissionClaims, apisv1alpha1.AcceptablePermissionClaim{
			PermissionClaim: claim,
			State:           apisv1alpha1.ClaimAccepted,
		})
	}
	oldJSON := encodeJSON(t, apiBinding)
	newJSON := encodeJSON(t, bindingWithClaims)
	patchBytes, err := jsonpatch.CreateMergePatch(oldJSON, newJSON)
	require.NoError(t, err, "error creating patch")
	apiBinding, err = kcpClusterClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	require.NoError(t, err, "error patching apibinding to accept all claims")

	t.Logf("waiting for apibinding status to show it's applied the claim for the sheriff's identity")
	framework.Eventually(t, func() (success bool, reason string) {
		// Get the binding and make sure that observed permission claims are all set.
		binding, err := kcpClusterClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		for _, claim := range binding.Status.AppliedPermissionClaims {
			if claim.IdentityHash == identityHash {
				return true, "found applied claim for identity"
			}
		}
		return false, "unable to find applied claim for identity"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to find applied permission claim for identityHash")

	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumerClusterName.Path(), "wild.wild.west", "in-vw")

	t.Logf("Expecting to eventually see all bindings")
	require.Eventually(t, func() bool {
		bindings, err := dynamicVWClusterClient.Resource(apisv1alpha1.SchemeGroupVersion.WithResource("apibindings")).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		return len(bindings.Items) > 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to see more than 1 binding")

	t.Logf("Verify that two sherrifs are eventually returned")
	var ul *unstructured.UnstructuredList

	framework.Eventually(t, func() (done bool, str string) {
		var err error
		ul, err = dynamicVWClusterClient.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
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

	var newClaims []apisv1alpha1.PermissionClaim
	for i := range apiExport.Spec.PermissionClaims {
		claim := apiExport.Spec.PermissionClaims[i]
		if claim.Group == "" && claim.Resource == "configmaps" {
			continue
		}
		newClaims = append(newClaims, claim)
	}

	updatedExport := apiExport.DeepCopy()
	updatedExport.Spec.PermissionClaims = newClaims

	oldJSON = encodeJSON(t, apiExport)
	newJSON = encodeJSON(t, updatedExport)
	patchBytes, err = jsonpatch.CreateMergePatch(oldJSON, newJSON)
	require.NoError(t, err, "error creating patch")
	t.Logf("patching apiexport to remove claim on configmaps")
	_, err = kcpClusterClient.Cluster(serviceProviderClusterName.Path()).ApisV1alpha1().APIExports().Patch(ctx, apiExport.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	require.NoError(t, err, "error patching apiexport")

	configMapsGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	t.Logf("making sure list configmaps is an error")
	framework.Eventually(t, func() (success bool, reason string) {
		_, err := dynamicVWClusterClient.Resource(configMapsGVR).List(ctx, metav1.ListOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("waiting for an IsNotFound error (%q)", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "didn't get list configmaps error")

	updatedAPIBinding := apiBinding.DeepCopy()
	for i := range updatedAPIBinding.Spec.PermissionClaims {
		claim := &updatedAPIBinding.Spec.PermissionClaims[i]
		if claim.Group == sheriffsGVR.Group && claim.Resource == sheriffsGVR.Resource {
			claim.State = apisv1alpha1.ClaimRejected
			break
		}
	}

	oldJSON = encodeJSON(t, apiBinding)
	newJSON = encodeJSON(t, updatedAPIBinding)
	patchBytes, err = jsonpatch.CreateMergePatch(oldJSON, newJSON)
	require.NoError(t, err, "error creating patch")
	t.Logf("patching apibinding to reject the sheriffs claim")
	_, err = kcpClusterClient.Cluster(consumerClusterName.Path()).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	require.NoError(t, err, "error patching apibinding")

	t.Logf("making sure list sheriffs is now empty")
	framework.Eventually(t, func() (success bool, reason string) {
		list, err := dynamicVWClusterClient.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing sheriffs")
		return len(list.Items) == 0, fmt.Sprintf("waiting for empty list, got %#v", list.Items)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to eventually get 0 sheriffs")
}

func TestAPIExportInternalAPIsDrift(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	cfg := server.BaseConfig(t)
	discoveryClient, err := kcpdiscovery.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct discovery client for server")

	orgClusterName := framework.NewOrganizationFixture(t, server)
	anyClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	apis, err := gatherInternalAPIs(discoveryClient.Cluster(anyClusterName.Path()), t)
	require.NoError(t, err, "failed to gather built-in apis for server")

	sort.Slice(apis, func(i, j int) bool {
		if apis[i].GroupVersion.String() == apis[j].GroupVersion.String() {
			return apis[i].Names.Plural < apis[j].Names.Plural
		}
		return apis[i].GroupVersion.String() < apis[j].GroupVersion.String()
	})

	expected := apiexportbuiltin.BuiltInAPIs

	sort.Slice(expected, func(i, j int) bool {
		if expected[i].GroupVersion.String() == expected[j].GroupVersion.String() {
			return expected[i].Names.Plural < expected[j].Names.Plural
		}
		return expected[i].GroupVersion.String() < expected[j].GroupVersion.String()
	})

	require.Empty(t, cmp.Diff(apis, expected))
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
		require.NoError(t, err)
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

func setUpServiceProviderWithPermissionClaims(ctx context.Context, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, identityHash string, t *testing.T) {
	claims := []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "serviceaccounts"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "apis.kcp.dev", Resource: "apibindings"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			IdentityHash:  identityHash,
			All:           true,
		},
	}
	setUpServiceProvider(ctx, dynamicClusterClient, kcpClients, serviceProviderWorkspace, cfg, t, claims...)
}

func setUpServiceProvider(ctx context.Context, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, serviceProviderWorkspace logicalcluster.Name, cfg *rest.Config, t *testing.T, claims ...apisv1alpha1.PermissionClaim) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
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
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func bindConsumerToProvider(ctx context.Context, consumerWorkspace logicalcluster.Name, providerClusterName tenancy.Cluster, t *testing.T, kcpClients kcpclientset.ClusterInterface, cfg *rest.Config, claims ...apisv1alpha1.AcceptablePermissionClaim) {
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerClusterName)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Cluster: providerClusterName,
					Name:    "today-cowboys",
				},
			},
			PermissionClaims: claims,
		},
	}

	consumerWsClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := consumerWsClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
	resources, err := consumerWsClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
}

func createCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Name, wildwestClusterClient wildwestclientset.ClusterInterface) {
	t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumer1Workspace)

	t.Logf("Make sure list shows nothing to start")
	cowboyClusterClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	var cowboys *wildwestv1alpha1.CowboyList
	// Adding a poll here to wait for the user's to get access via RBAC informer updates.

	require.Eventually(t, func() bool {
		var err error
		cowboys, err = cowboyClusterClient.List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list ")
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside consumer workspace %q", consumer1Workspace)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumer1Workspace)
	cowboyName := fmt.Sprintf("cowboy-%s", consumer1Workspace.Base())
	cowboy := newCowboy("default", cowboyName)
	_, err := cowboyClusterClient.Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumer1Workspace)
}

func newCowboy(namespace, name string) *wildwestv1alpha1.Cowboy {
	return &wildwestv1alpha1.Cowboy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: wildwestv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Cowboy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
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

func encodeJSON(t *testing.T, obj interface{}) []byte {
	ret, err := json.Marshal(obj)
	require.NoError(t, err)
	return ret
}
