/*
Copyright 2026 The kcp Authors.

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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestScaleSubresourceThroughVW(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	claimerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	cowboysGVR := schema.GroupVersionResource{Group: "scale.wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}
	consumerCowboys := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default")

	t.Log("Install cowboys APIResourceSchema with status and scale subresources into provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys_scale.yaml", testFiles)
	require.NoError(t, err)

	t.Log("Create APIExport in provider")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "scale-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "scale.wildwest.dev",
					Schema: "today.cowboys.scale.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind provider APIExport in consumer")
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a cowboy in the consumer workspace")
	cowboy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "scale.wildwest.dev/v1alpha1",
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name": "woody",
			},
			"spec": map[string]interface{}{
				"intent":   "yeehaw",
				"replicas": int64(1),
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := consumerCowboys.Create(t.Context(), cowboy, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating cowboy: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for VW URL of the provider APIExport")
	providerVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		providerVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	providerVWClient, err := kcpdynamic.NewForConfig(providerVWCfg)
	require.NoError(t, err)
	providerVWCowboys := providerVWClient.Cluster(consumerClusterName.Path()).Resource(cowboysGVR).Namespace("default")

	t.Log("Update the status subresource through the provider VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		vwCowboy, err := providerVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy through VW: %v", err)
		}
		if err := unstructured.SetNestedField(vwCowboy.Object, "giddyup", "status", "result"); err != nil {
			return false, err.Error()
		}
		_, err = providerVWCowboys.Update(t.Context(), vwCowboy, metav1.UpdateOptions{}, "status")
		return err == nil, fmt.Sprintf("error updating cowboy status through VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the status update is visible in the consumer workspace")
	updated, err := consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	result, _, err := unstructured.NestedString(updated.Object, "status", "result")
	require.NoError(t, err)
	require.Equal(t, "giddyup", result)

	t.Log("Update the scale subresource through the provider VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		scale, err := providerVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{}, "scale")
		if err != nil {
			return false, fmt.Sprintf("error getting scale through VW: %v", err)
		}
		if err := unstructured.SetNestedField(scale.Object, int64(3), "spec", "replicas"); err != nil {
			return false, err.Error()
		}
		_, err = providerVWCowboys.Update(t.Context(), scale, metav1.UpdateOptions{}, "scale")
		return err == nil, fmt.Sprintf("error updating scale through VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the scale update is visible in the consumer workspace")
	updated, err = consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, err := unstructured.NestedInt64(updated.Object, "spec", "replicas")
	require.NoError(t, err)
	require.Equal(t, int64(3), replicas)

	t.Log("Get the provider APIExport identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	export, err := kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := export.Status.IdentityHash

	t.Log("Create claimer APIExport in third workspace claiming cowboys and cowboys/scale")
	cowboysClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    "scale.wildwest.dev",
			Resource: "cowboys",
		},
		Verbs:        []string{"get", "list", "watch", "update", "patch"},
		IdentityHash: identityHash,
	}
	scaleClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    "scale.wildwest.dev",
			Resource: "cowboys/scale",
		},
		Verbs:        []string{"get", "update", "patch"},
		IdentityHash: identityHash,
	}
	claimerExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboy-wrangler",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				cowboysClaim,
				scaleClaim,
			},
		},
	}
	_, err = kcpClients.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(t.Context(), claimerExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind claimer APIExport in consumer, accepting the claims")
	claimerBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: claimerExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: claimerPath.String(),
					Name: claimerExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: cowboysClaim,
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
				},
				{
					State: apisv1alpha2.ClaimAccepted,
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: scaleClaim,
					},
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), claimerBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for VW URL of the claimer APIExport")
	claimerVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(claimerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), claimerExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		claimerVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	claimerVWClient, err := kcpdynamic.NewForConfig(claimerVWCfg)
	require.NoError(t, err)
	claimerVWCowboys := claimerVWClient.Cluster(consumerClusterName.Path()).Resource(cowboysGVR).Namespace("default")

	t.Log("Update the cowboy through the claimer VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		vwCowboy, err := claimerVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy through claimer VW: %v", err)
		}
		if err := unstructured.SetNestedField(vwCowboy.Object, "howdy", "spec", "intent"); err != nil {
			return false, err.Error()
		}
		_, err = claimerVWCowboys.Update(t.Context(), vwCowboy, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("error updating cowboy through claimer VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the cowboy update is visible in the consumer workspace")
	updated, err = consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	intent, _, err := unstructured.NestedString(updated.Object, "spec", "intent")
	require.NoError(t, err)
	require.Equal(t, "howdy", intent)

	t.Log("Update the scale subresource through the claimer VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		scale, err := claimerVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{}, "scale")
		if err != nil {
			return false, fmt.Sprintf("error getting scale through claimer VW: %v", err)
		}
		if err := unstructured.SetNestedField(scale.Object, int64(5), "spec", "replicas"); err != nil {
			return false, err.Error()
		}
		_, err = claimerVWCowboys.Update(t.Context(), scale, metav1.UpdateOptions{}, "scale")
		return err == nil, fmt.Sprintf("error updating scale through claimer VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the scale update is visible in the consumer workspace")
	updated, err = consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, err = unstructured.NestedInt64(updated.Object, "spec", "replicas")
	require.NoError(t, err)
	require.Equal(t, int64(5), replicas)
}
