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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestStatusSubresourceClaimsThroughVW(t *testing.T) {
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

	cowboysGVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}
	consumerCowboys := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default")

	t.Log("Install cowboys APIResourceSchema with status subresource into provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Log("Create APIExport in provider")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "status-cowboys",
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
			"apiVersion": "wildwest.dev/v1alpha1",
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name": "woody",
			},
			"spec": map[string]interface{}{
				"intent": "yeehaw",
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := consumerCowboys.Create(t.Context(), cowboy, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating cowboy: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Get the provider APIExport identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	export, err := kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := export.Status.IdentityHash

	cowboysClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    "wildwest.dev",
			Resource: "cowboys",
		},
		Verbs:        []string{"get", "list", "watch", "update", "patch"},
		IdentityHash: identityHash,
	}

	t.Log("Create claimer APIExport claiming only the parent cowboys resource")
	implicitExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "implicit-status-wrangler",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				cowboysClaim,
			},
		},
	}
	_, err = kcpClients.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(t.Context(), implicitExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind implicit claimer APIExport in consumer, accepting the parent claim")
	implicitBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: implicitExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: claimerPath.String(),
					Name: implicitExport.Name,
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
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), implicitBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating implicit claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	implicitVWCowboys := vwCowboysClient(t, cfg, kcpClients, consumerWorkspace, claimerPath, implicitExport.Name, cowboysGVR)

	t.Log("Update the status subresource through the implicit claimer VW, inherited from the parent claim")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		vwCowboy, err := implicitVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy through implicit claimer VW: %v", err)
		}
		if err := unstructured.SetNestedField(vwCowboy.Object, "giddyup", "status", "result"); err != nil {
			return false, err.Error()
		}
		_, err = implicitVWCowboys.Update(t.Context(), vwCowboy, metav1.UpdateOptions{}, "status")
		return err == nil, fmt.Sprintf("error updating cowboy status through implicit claimer VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the status update is visible in the consumer workspace")
	updated, err := consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	result, _, err := unstructured.NestedString(updated.Object, "status", "result")
	require.NoError(t, err)
	require.Equal(t, "giddyup", result)

	statusClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    "wildwest.dev",
			Resource: "cowboys/status",
		},
		Verbs:        []string{"get", "update", "patch"},
		IdentityHash: identityHash,
	}

	t.Log("Create claimer APIExport claiming cowboys and cowboys/status explicitly")
	explicitExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "explicit-status-wrangler",
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				cowboysClaim,
				statusClaim,
			},
		},
	}
	_, err = kcpClients.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(t.Context(), explicitExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind explicit claimer APIExport in consumer, accepting only the parent claim")
	explicitBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: explicitExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: claimerPath.String(),
					Name: explicitExport.Name,
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
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), explicitBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating explicit claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	explicitVWCowboys := vwCowboysClient(t, cfg, kcpClients, consumerWorkspace, claimerPath, explicitExport.Name, cowboysGVR)

	t.Log("Wait until the cowboy is readable through the explicit claimer VW")
	var vwCowboy *unstructured.Unstructured
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		vwCowboy, err = explicitVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		return err == nil, fmt.Sprintf("error getting cowboy through explicit claimer VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify updating the status subresource is forbidden while the status claim is not accepted")
	err = unstructured.SetNestedField(vwCowboy.Object, "denied", "status", "result")
	require.NoError(t, err)
	_, err = explicitVWCowboys.Update(t.Context(), vwCowboy, metav1.UpdateOptions{}, "status")
	require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)

	t.Log("Accept the status claim on the explicit claimer APIBinding")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), explicitBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting explicit claimer APIBinding: %v", err)
		}
		binding.Spec.PermissionClaims = append(binding.Spec.PermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
			State: apisv1alpha2.ClaimAccepted,
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: statusClaim,
			},
		})
		_, err = kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Update(t.Context(), binding, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("error updating explicit claimer APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Update the status subresource through the explicit claimer VW")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		vwCowboy, err := explicitVWCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy through explicit claimer VW: %v", err)
		}
		if err := unstructured.SetNestedField(vwCowboy.Object, "claimed", "status", "result"); err != nil {
			return false, err.Error()
		}
		_, err = explicitVWCowboys.Update(t.Context(), vwCowboy, metav1.UpdateOptions{}, "status")
		return err == nil, fmt.Sprintf("error updating cowboy status through explicit claimer VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Verify the status update is visible in the consumer workspace")
	updated, err = consumerCowboys.Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	result, _, err = unstructured.NestedString(updated.Object, "status", "result")
	require.NoError(t, err)
	require.Equal(t, "claimed", result)
}

// vwCowboysClient waits for the APIExport VW URL and returns a namespaced
// dynamic client for gvr in the consumer workspace.
func vwCowboysClient(t *testing.T, cfg *rest.Config, kcpClients kcpclientset.ClusterInterface, consumerWorkspace *tenancyv1alpha1.Workspace, exportPath logicalcluster.Path, exportName string, gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	t.Helper()

	vwCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(exportPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), exportName, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		vwCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	vwClient, err := kcpdynamic.NewForConfig(vwCfg)
	require.NoError(t, err)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)
	return vwClient.Cluster(consumerClusterName.Path()).Resource(gvr).Namespace("default")
}
