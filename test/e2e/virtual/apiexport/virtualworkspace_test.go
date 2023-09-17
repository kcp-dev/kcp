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
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/yaml"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/config/helpers"
	kcpscheme "github.com/kcp-dev/kcp/pkg/server/scheme"
	apiexportbuiltin "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestv1alpha1ac "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/applyconfiguration/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/logicalcluster/v3"
)

func TestAPIExportVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	serviceProviderPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, serviceProviderPath, []string{"user-1"}, nil, false)

	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClients, serviceProviderPath, cfg)
	bindConsumerToProvider(ctx, t, consumerPath, serviceProviderPath, kcpClients, cfg)
	createCowboyInConsumer(ctx, t, consumerPath, wildwestClusterClient)

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerWorkspace.Name)
	apiExportVWCfg := rest.CopyConfig(cfg)
	framework.Eventually(t, func() (bool, string) {
		apiExport, err := kcpClients.Cluster(serviceProviderPath).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExport))
		require.NoError(t, err)
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExport.Status.VirtualWorkspaces)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Verifying that the virtual workspace includes the cowboy resource")
	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	t.Logf("Verify that the virtual workspace includes apibindings")
	discoveryVCClusterClient, err := kcpdiscovery.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	resources, err := discoveryVCClusterClient.ServerResourcesForGroupVersion(apisv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving APIExport discovery")
	require.True(t, resourceExists(resources, "apibindings"), "missing apibindings")

	user1VWCfg := framework.StaticTokenUserConfig("user-1", apiExportVWCfg)
	wwUser1VC, err := wildwestclientset.NewForConfig(user1VWCfg)
	require.NoError(t, err)

	t.Logf("=== list ===")
	var cowboy *wildwestv1alpha1.Cowboy
	{
		t.Logf("Verify that user-1 cannot list")
		_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Verify that user-1 cannot wildcard list")
		_, err = wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 get+list access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(serviceProviderPath), "user-1-vw-list-get", "user-1", "User", []string{"list", "get"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")

		t.Logf("Verify that user-1 can now wildcard list cowboys")
		framework.Eventually(t, func() (bool, string) {
			cbs, err := wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			require.Len(t, cbs.Items, 1, "expected to find exactly one cowboy")
			cowboy = &cbs.Items[0]
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys")

		t.Logf("Verify that user-1 can now list cowboys")
		cbs, err := wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, cbs.Items, 1, "expected to find exactly one cowboy")
	}

	t.Logf("=== update ===")
	{
		t.Logf("Verify that user-1 cannot update cowboys")
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
			require.NoError(t, err)
			cowboy.Annotations["foo"] = "bar"
			_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Update(ctx, cowboy, metav1.UpdateOptions{})
			return err
		})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Verify that user-1 cannot update status of cowboys")
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
			require.NoError(t, err)
			cowboy.Status.Result = "updated"
			_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).UpdateStatus(ctx, cowboy, metav1.UpdateOptions{})
			return err
		})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 update access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(serviceProviderPath), "user-1-vw-update", "user-1", "User", []string{"update"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")

		t.Logf("Verify that user-1 can now update cowboys")
		framework.Eventually(t, func() (bool, string) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
				require.NoError(t, err)
				cowboy.Annotations["foo"] = "bar"
				_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Update(ctx, cowboy, metav1.UpdateOptions{})
				return err
			})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to update cowboys")

		t.Logf("Verify that user-1 can now update status of cowboys")
		framework.Eventually(t, func() (bool, string) {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cowboy, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
				require.NoError(t, err)
				cowboy.Status.Result = "updated"
				_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).UpdateStatus(ctx, cowboy, metav1.UpdateOptions{})
				return err
			})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to update status of cowboys")
	}

	t.Logf("=== create ===")
	{
		t.Logf("Verify that user-1 cannot create cowboys")
		_, err := wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, newCowboy("default", "another"), metav1.CreateOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 create access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(serviceProviderPath), "user-1-vw-create", "user-1", "User", []string{"create"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")

		t.Logf("Verify that user-1 can now create cowboys")
		framework.Eventually(t, func() (bool, string) {
			_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, newCowboy("default", "another"), metav1.CreateOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to create a cowboy")
	}

	t.Logf("=== patch ===")
	{
		t.Logf("Verify that user-1 cannot patch cowboys")
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			patch := `{"spec":{"intent":"3"}}`
			_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Patch(ctx, cowboy.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			return err
		})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 patch access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(serviceProviderPath), "user-1-vw-patch", "user-1", "User", []string{"patch"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")

		t.Logf("Verify that user-1 can now patch (application/merge-patch+json) cowboys")
		framework.Eventually(t, func() (bool, string) {
			patch := `{"spec":{"intent":"3"}}`
			_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Patch(ctx, cowboy.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to patch a cowboy")

		t.Logf("Verify that user-1 can now patch (application/apply-patch+yaml) cowboys")
		patch := `{"apiVersion":"wildwest.dev/v1alpha1","kind":"Cowboy","spec":{"intent":"4"}}`
		_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys(cowboy.Namespace).Patch(ctx, cowboy.Name, types.ApplyPatchType, []byte(patch), metav1.PatchOptions{FieldManager: "e2e-test-runner", Force: pointer.Bool(true)})
		require.NoError(t, err)

		t.Logf("Verify that user-1 can not patch a non-existent cowboy")
		patch = `{"spec":{"intent":"5"}}`
		_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "nonexisting", types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		require.True(t, apierrors.IsNotFound(err))

		t.Logf("Verify that user-1 can patch a non-existent cowboy with SSA")
		_, err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
			Apply(ctx, wildwestv1alpha1ac.Cowboy("nonexisting-ssa", "default").
				WithSpec(wildwestv1alpha1ac.CowboySpec().WithIntent("6")),
				metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
		require.NoError(t, err)
	}

	t.Logf("=== delete ===")
	{
		t.Logf("Verify that user-1 cannot delete cowboys")
		err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Delete(ctx, "another", metav1.DeleteOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 delete access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(serviceProviderPath), "user-1-vw-delete", "user-1", "User", []string{"delete", "deletecollection"}, apisv1alpha1.SchemeGroupVersion.Group, "apiexports/content", "")

		t.Logf("Verify that user-1 can now delete cowboys")
		framework.Eventually(t, func() (bool, string) {
			err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Delete(ctx, "another", metav1.DeleteOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to delete a cowboy")

		t.Logf("Verify that deleted cowboy is gone")
		cowboys, err := wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Equal(t, 2, len(cowboys.Items))
		names := make([]string, 0, len(cowboys.Items))
		for _, c := range cowboys.Items {
			names = append(names, c.Name)
		}
		require.ElementsMatch(t, []string{cowboy.Name, "nonexisting-ssa"}, names)

		t.Logf("Verify that user-1 can delete all cowboys")
		err = wwUser1VC.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
		require.NoError(t, err)

		t.Logf("Verify that all cowboys are gone")
		cowboys, err = wwUser1VC.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Equal(t, 0, len(cowboys.Items))
	}
}

func TestAPIExportAPIBindingsAccess(t *testing.T) {
	t.Skip("https://github.com/kcp-dev/kcp/issues/2263")
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	ws1Path, ws1 := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("workspace1"))
	ws2Path, ws2 := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("workspace2"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	mappers := make(map[logicalcluster.Path]meta.RESTMapper)
	mappers[ws1Path] = restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(kcpClusterClient.Cluster(ws1Path).Discovery()),
	)
	mappers[ws2Path] = restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(kcpClusterClient.Cluster(ws2Path).Discovery()),
	)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	create := func(clusterName logicalcluster.Path, msg, file string, transforms ...helpers.TransformFileFunc) {
		t.Helper()
		t.Logf("%s: creating %s", clusterName, msg)
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(clusterName), mappers[clusterName], nil, file, testFiles, transforms...)
		require.NoError(t, err, "%s: error creating %s", clusterName, msg)
	}

	create(ws1Path, "APIResourceSchema 1", "apibindings_access_schema1.yaml")
	create(ws1Path, "APIExport 1", "apibindings_access_export1.yaml")
	create(ws1Path, "APIResourceSchema 2", "apibindings_access_schema2.yaml")
	create(ws1Path, "APIExport 2", "apibindings_access_export2.yaml")
	create(ws1Path, "APIBinding referencing APIExport 1", "apibindings_access_binding1.yaml")
	create(ws1Path, "APIBinding referencing APIExport 2", "apibindings_access_binding2.yaml")

	create(ws2Path, "APIBinding referencing APIExport 1", "apibindings_access_binding1.yaml", func(bs []byte) ([]byte, error) {
		var binding apisv1alpha1.APIBinding
		err := yaml.Unmarshal(bs, &binding)
		require.NoError(t, err, "error unmarshaling binding")
		binding.Spec.Reference.Export.Path = ws1Path.String()
		out, err := yaml.Marshal(&binding)
		require.NoError(t, err, "error marshaling binding")
		return out, nil
	})
	create(ws2Path, "APIResourceSchema 3", "apibindings_access_schema3.yaml")
	create(ws2Path, "APIExport 3", "apibindings_access_export3.yaml")
	create(ws2Path, "APIBinding referencing APIExport 3", "apibindings_access_binding3.yaml")

	exportURLs := make(map[string]string)

	verifyBindings := func(clusterName logicalcluster.Path, exportName string, isValid func(bindings []apisv1alpha1.APIBinding) error) {
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
	verifyBindings(ws1Path, "export1", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := logicalcluster.From(&b)
			if clusterName == logicalcluster.Name(ws1.Spec.Cluster) && b.Name == "binding1" {
				continue
			}
			if clusterName == logicalcluster.Name(ws2.Spec.Cluster) && b.Name == "binding1" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Verifying APIExport 2 only serves APIBinding 2")
	verifyBindings(ws1Path, "export2", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := logicalcluster.From(&b)
			if clusterName == logicalcluster.Name(ws1.Spec.Cluster) && b.Name == "binding2" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Updating APIExport 1 to claim APIBindings")
	export1, err := kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIExports().Get(ctx, "export1", metav1.GetOptions{})
	require.NoError(t, err)
	export1.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    "apis.kcp.io",
				Resource: "apibindings",
			},
			All: true,
		},
	}
	_, err = kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIExports().Update(ctx, export1, metav1.UpdateOptions{})
	require.NoError(t, err)

	t.Logf("Updating APIBinding 1 to accept APIBindings claim")
	binding1, err := kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIBindings().Get(ctx, "binding1", metav1.GetOptions{})
	require.NoError(t, err)
	oldData := toJSON(t, apisv1alpha1.APIBinding{})
	newData := toJSON(t, apisv1alpha1.APIBinding{
		Spec: apisv1alpha1.APIBindingSpec{
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: export1.Spec.PermissionClaims[0],
					State:           apisv1alpha1.ClaimAccepted,
				},
			},
		},
	})
	patchBytes, err := jsonpatch.CreateMergePatch([]byte(oldData), []byte(newData))
	require.NoError(t, err, "error creating patch")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIBindings().Patch(ctx, binding1.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			return true, ""
		}
		require.False(t, apierrors.IsConflict(err))
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating %s|%s to accept claims", ws1Path, binding1.Name)

	t.Logf("Verifying APIExport 1 serves APIBindings 1|1, 1|2, and 2|1")
	verifyBindings(ws1Path, "export1", func(bindings []apisv1alpha1.APIBinding) error {
		// "workspace1|binding1", "workspace2|binding1", "workspace1|binding2")
		actualWorkspace1 := sets.New[string]()

		for _, b := range bindings {
			clusterName := logicalcluster.From(&b)
			if clusterName != logicalcluster.Name(ws1.Spec.Cluster) {
				if clusterName == logicalcluster.Name(ws2.Spec.Cluster) && b.Name == "binding1" {
					continue
				}

				return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
			}

			actualWorkspace1.Insert(b.Name)
		}

		expectedWorkspace1 := sets.New[string]("binding1", "binding2")
		if !actualWorkspace1.IsSuperset(expectedWorkspace1) {
			return fmt.Errorf("mismatch %s", cmp.Diff(expectedWorkspace1, actualWorkspace1))
		}

		return nil
	})

	t.Logf("Verifying APIExport 2 still only serves APIBinding 2")
	verifyBindings(ws1Path, "export2", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := logicalcluster.From(&b)
			if clusterName == logicalcluster.Name(ws1.Spec.Cluster) && b.Name == "binding2" {
				continue
			}

			return fmt.Errorf("unexpected binding %s|%s", clusterName, b.Name)
		}

		return nil
	})

	t.Logf("Updating APIBinding 1 to reject APIBindings claim")
	binding1, err = kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIBindings().Get(ctx, "binding1", metav1.GetOptions{})
	require.NoError(t, err)
	oldData = toJSON(t, binding1)
	binding1.Spec.PermissionClaims = nil
	newData = toJSON(t, binding1)
	patchBytes, err = jsonpatch.CreateMergePatch([]byte(oldData), []byte(newData))
	require.NoError(t, err, "error creating patch")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(ws1Path).ApisV1alpha1().APIBindings().Patch(ctx, binding1.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err == nil {
			return true, ""
		}
		require.False(t, apierrors.IsConflict(err))
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating %s|%s to no longer accept claims", ws1Path, binding1.Name)

	t.Logf("Verifying APIExport 1 back to only serving its own bindings")
	verifyBindings(ws1Path, "export1", func(bindings []apisv1alpha1.APIBinding) error {
		for _, b := range bindings {
			clusterName := logicalcluster.From(&b)
			if clusterName == logicalcluster.Name(ws1.Spec.Cluster) && b.Name == "binding1" {
				continue
			}
			if clusterName == logicalcluster.Name(ws2.Spec.Cluster) && b.Name == "binding1" {
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
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	claimerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("claimer"))
	sheriffProviderPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("provider"))
	serviceProviderSheriffsNotUsed, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("provider-unused"))
	consumer1Path, consumer1 := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer1"))
	consumer2Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer2"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")
	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, sheriffProviderPath, kcpClusterClient, "wild.wild.west", "board the wanderer")
	// Creating extra export, to make sure the correct one is always used.
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderSheriffsNotUsed, kcpClusterClient, "wild.wild.west", "use the giant spider")

	t.Logf("get the sheriffs apiexport's generated identity hash")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(sheriffProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.APIExportIdentityValid))

	sheriffExport, err := kcpClusterClient.Cluster(sheriffProviderPath).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := sheriffExport.Status.IdentityHash
	t.Logf("Found identity hash: %v", identityHash)

	t.Logf("Bind sheriffs into %s and create initial sheriff", consumer1Path)
	apifixtures.BindToExport(ctx, t, sheriffProviderPath, "wild.wild.west", consumer1Path, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumer1Path, "wild.wild.west", "in-vw-before")

	t.Logf("Bind sheriffs into %s and create initial sheriff", consumer2Path)
	apifixtures.BindToExport(ctx, t, sheriffProviderPath, "wild.wild.west", consumer2Path, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumer2Path, "wild.wild.west", "not-in-vw")

	t.Logf("Create cowyboys API Export in %v with permission claims to core resources and sheriffs provided by %v", claimerPath, sheriffProviderPath)
	setUpServiceProviderWithPermissionClaims(ctx, t, dynamicClusterClient, kcpClusterClient, claimerPath, cfg, identityHash)

	t.Logf("Bind cowboys from %v to %v and create cowboy", claimerPath, consumer1Path)
	bindConsumerToProvider(ctx, t, consumer1Path, claimerPath, kcpClusterClient, cfg)
	createCowboyInConsumer(ctx, t, consumer1Path, wildwestClusterClient)

	t.Logf("Waiting for cowboy APIExport to have a virtual workspace URL for the bound workspaces %q", consumer1Path)
	consumer1VWCfg := rest.CopyConfig(cfg)
	framework.Eventually(t, func() (bool, string) {
		apiExport, err := kcpClusterClient.Cluster(claimerPath).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		consumer1VWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumer1, framework.ExportVirtualWorkspaceURLs(apiExport))
		require.NoError(t, err)
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExport.Status.VirtualWorkspaces)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Verify that the exported resource is retrievable")
	wildwestVCClients, err := wildwestclientset.NewForConfig(consumer1VWCfg)
	require.NoError(t, err)
	cowboys, err := wildwestVCClients.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboys.Items))

	t.Logf("Verify that we get empty lists for all claimed resources (other than apibindings) because the claims have not been accepted yet")
	dynamicVWClusterClient, err := kcpdynamic.NewForConfig(consumer1VWCfg)
	require.NoError(t, err)
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

	t.Logf("Patching apibinding to accept all permission claims")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiBinding, err := kcpClusterClient.Cluster(consumer1Path).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err, "error getting cowboys apibinding")
		bindingWithClaims := apiBinding.DeepCopy()
		for i := range apiBinding.Status.ExportPermissionClaims {
			claim := apiBinding.Status.ExportPermissionClaims[i]
			bindingWithClaims.Spec.PermissionClaims = append(bindingWithClaims.Spec.PermissionClaims, apisv1alpha1.AcceptablePermissionClaim{
				PermissionClaim: claim,
				State:           apisv1alpha1.ClaimAccepted,
			})
		}
		oldJSON := toJSON(t, apiBinding)
		newJSON := toJSON(t, bindingWithClaims)
		patchBytes, err := jsonpatch.CreateMergePatch([]byte(oldJSON), []byte(newJSON))
		require.NoError(t, err, "error creating patch")
		_, err = kcpClusterClient.Cluster(consumer1Path).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
	require.NoError(t, err, "error patching apibinding to accept all claims")

	t.Logf("Waiting for apibinding status to show applied claims for the sheriff's identity")
	framework.Eventually(t, func() (success bool, reason string) {
		// Get the binding and make sure that observed permission claims are all set.
		binding, err := kcpClusterClient.Cluster(consumer1Path).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		for _, claim := range binding.Status.AppliedPermissionClaims {
			if claim.IdentityHash == identityHash {
				return true, "found applied claim for identity"
			}
		}
		return false, "unable to find applied claim for identity"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to find applied permission claim for identityHash")

	t.Logf("Expecting to eventually see all bindings")
	require.Eventually(t, func() bool {
		bindings, err := dynamicVWClusterClient.Resource(apisv1alpha1.SchemeGroupVersion.WithResource("apibindings")).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		return len(bindings.Items) > 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to see more than 1 binding")

	t.Logf("Creating a sheriff in %s", consumer1Path)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, consumer1Path, "wild.wild.west", "in-vw")

	t.Logf("Verify that two sherrifs are eventually returned")
	framework.Eventually(t, func() (done bool, str string) {
		ul, err := dynamicVWClusterClient.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		var names []string
		for _, item := range ul.Items {
			names = append(names, item.GetName())
		}
		sort.Strings(names)
		expected := []string{"in-vw", "in-vw-before"}
		sort.Strings(expected)
		if diff := cmp.Diff(expected, names); diff != "" {
			return false, fmt.Sprintf("did not find both objects: %v", diff)
		}

		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to wait for the two objects to be returned")

	t.Logf("Remove claim on configmaps from apiexport")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiExport, err := kcpClusterClient.Cluster(claimerPath).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		newClaims := make([]apisv1alpha1.PermissionClaim, 0, len(apiExport.Spec.PermissionClaims))
		for i := range apiExport.Spec.PermissionClaims {
			claim := apiExport.Spec.PermissionClaims[i]
			if claim.Group == "" && claim.Resource == "configmaps" {
				continue
			}
			newClaims = append(newClaims, claim)
		}
		updatedExport := apiExport.DeepCopy()
		updatedExport.Spec.PermissionClaims = newClaims

		oldJSON := toJSON(t, apiExport)
		newJSON := toJSON(t, updatedExport)
		patchBytes, err := jsonpatch.CreateMergePatch([]byte(oldJSON), []byte(newJSON))
		require.NoError(t, err, "error creating patch")

		_, err = kcpClusterClient.Cluster(claimerPath).ApisV1alpha1().APIExports().Patch(ctx, apiExport.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
	require.NoError(t, err)

	t.Logf("Making sure list configmaps is an error")
	framework.Eventually(t, func() (success bool, reason string) {
		_, err := dynamicVWClusterClient.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}).List(ctx, metav1.ListOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("waiting for an IsNotFound error, but got: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "didn't get list configmaps error")

	t.Logf("Rejecting the sheriffs claim")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiBinding, err := kcpClusterClient.Cluster(consumer1Path).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)

		updatedAPIBinding := apiBinding.DeepCopy()
		for i := range updatedAPIBinding.Spec.PermissionClaims {
			claim := &updatedAPIBinding.Spec.PermissionClaims[i]
			if claim.Group == sheriffsGVR.Group && claim.Resource == sheriffsGVR.Resource {
				claim.State = apisv1alpha1.ClaimRejected
				break
			}
		}

		oldJSON := toJSON(t, apiBinding)
		newJSON := toJSON(t, updatedAPIBinding)
		patchBytes, err := jsonpatch.CreateMergePatch([]byte(oldJSON), []byte(newJSON))
		require.NoError(t, err, "error creating patch")

		_, err = kcpClusterClient.Cluster(consumer1Path).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
		return err
	})
	require.NoError(t, err, "error patching apibinding")

	t.Logf("Verify that the list of sheriffs becomes empty")
	framework.Eventually(t, func() (success bool, reason string) {
		list, err := dynamicVWClusterClient.Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		return len(list.Items) == 0, fmt.Sprintf("waiting for empty list, got: %#v", list.Items)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to eventually get 0 sheriffs")
}

func TestAPIExportInternalAPIsDrift(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	cfg := server.BaseConfig(t)
	discoveryClient, err := kcpdiscovery.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct discovery client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	anyPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	apis, err := gatherInternalAPIs(t, discoveryClient.Cluster(anyPath))
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

func gatherInternalAPIs(t *testing.T, discoveryClient discovery.DiscoveryInterface) ([]internalapis.InternalAPI, error) {
	t.Helper()

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
		if strings.HasSuffix(gv.Group, ".kcp.io") {
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

			instance, err := kcpscheme.Scheme.New(gvk)
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

func setUpServiceProviderWithPermissionClaims(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, serviceProviderWorkspace logicalcluster.Path, cfg *rest.Config, identityHash string) {
	t.Helper()

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
			GroupResource: apisv1alpha1.GroupResource{Group: "apis.kcp.io", Resource: "apibindings"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			IdentityHash:  identityHash,
			All:           true,
		},
	}
	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClients, serviceProviderWorkspace, cfg, claims...)
}

func setUpServiceProvider(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, serviceProviderWorkspace logicalcluster.Path, cfg *rest.Config, claims ...apisv1alpha1.PermissionClaim) {
	t.Helper()
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

func bindConsumerToProvider(ctx context.Context, t *testing.T, consumerWorkspace logicalcluster.Path, providerPath logicalcluster.Path, kcpClients kcpclientset.ClusterInterface, cfg *rest.Config, claims ...apisv1alpha1.AcceptablePermissionClaim) {
	t.Helper()
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerPath)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
			PermissionClaims: claims,
		},
	}

	consumerWsClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
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

func createCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface) {
	t.Helper()
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

func admit(t *testing.T, kubeClusterClient kubernetesclientset.Interface, ruleName, subjectName, subjectKind string, verbs []string, resources ...string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cr, crb := createClusterRoleAndBindings(ruleName, subjectName, subjectKind, verbs, resources...)
	_, err := kubeClusterClient.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
}

func toJSON(t *testing.T, obj interface{}) string {
	t.Helper()
	ret, err := json.Marshal(obj)
	require.NoError(t, err)
	return string(ret)
}
