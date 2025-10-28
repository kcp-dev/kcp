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

package apibinding

import (
	"fmt"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Test that service provider can access logical cluster object from within the
// consumers cluster when consumer binds to the provider's APIExport.
func TestAPIBindingLogicalCluster(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("providerPath: %v", providerPath)
	t.Logf("consumerPath: %v", consumerPath)

	ctx := t.Context()

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	_, err = kcpClusterClient.Cluster(providerPath).CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "failed to list logical clusters")

	exportName := "logical-clusters"
	apiExport := apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "core.kcp.io",
						Resource: "logicalclusters",
					},
					Verbs: []string{"*"},
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api export")

	t.Logf("validate that the permission claim's conditions true")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	apiBinding := apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: exportName,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "core.kcp.io",
								Resource: "logicalclusters",
							},
							Verbs: []string{"*"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("failed to create api binding: %v", err)
	}, wait.ForeverTestTimeout, time.Second*1, "failed to create api binding")

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")

	t.Logf("Waiting for APIExportEndpointSlice to be available")
	var exportEndpointSlice *v1alpha1.APIExportEndpointSlice
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		exportEndpointSlice, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, exportName, metav1.GetOptions{})
		return err == nil && len(exportEndpointSlice.Status.APIExportEndpoints) > 0, fmt.Sprintf("failed to get APIExportEndpointSlice: %v, %s", err, spew.Sdump(exportEndpointSlice))
	}, wait.ForeverTestTimeout, time.Second*1)

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	gvr := corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		items := []unstructured.Unstructured{}

		for _, vw := range exportEndpointSlice.Status.APIExportEndpoints {
			vwClusterClient, err := kcpdynamic.NewForConfig(apiexportVWConfig(t, rawConfig, vw.URL))
			require.NoError(t, err)

			list, err := vwClusterClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("Error listing LogicalClusters on %s: %v", vw.URL, err)
			}

			if list != nil {
				for _, item := range list.Items {
					// Sorry :( Checking the owner to validate
					name := item.Object["spec"].(map[string]interface{})["owner"].(map[string]interface{})["name"].(string)
					if name != consumerPath.Base() {
						t.Logf("found item not matching owner: got %s, expected %s", name, consumerPath.Base())
						continue
					}
					items = append(items, item)
				}
			}
		}

		return len(items) == 1, fmt.Sprintf("Unexpected number of LogicalClusters found. Expected 1, found %d", len(items))
	}, wait.ForeverTestTimeout, time.Second*1)
}

func TestAPIBindingCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("providerPath: %v", providerPath)
	t.Logf("consumerPath: %v", consumerPath)

	ctx := t.Context()

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	// create test crd in the consumer cluster
	cowBoysGR := metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}
	t.Log("Creating wildwest.dev CRD")
	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcpCRDClusterClient := kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	t.Log("Creating wildwest.dev.cowboys CR")
	wildwest.Create(t, consumerPath, kcpCRDClusterClient, cowBoysGR)

	exportName := "crds"
	apiExport := apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "apiextensions.k8s.io",
						Resource: "customresourcedefinitions",
					},
					Verbs: []string{"*"},
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api export")

	t.Logf("validate that the permission claim's conditions true")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	apiBinding := apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: exportName,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "apiextensions.k8s.io",
								Resource: "customresourcedefinitions",
							},
							Verbs: []string{"*"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("failed to create api binding: %v", err)
	}, wait.ForeverTestTimeout, time.Second*1, "failed to create api binding")

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")

	t.Logf("Waiting for APIExportEndpointSlice to be available")
	var exportEndpointSlice *v1alpha1.APIExportEndpointSlice
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		exportEndpointSlice, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, exportName, metav1.GetOptions{})
		return err == nil && len(exportEndpointSlice.Status.APIExportEndpoints) > 0, fmt.Sprintf("failed to get APIExportEndpointSlice: %v. %s", err, spew.Sdump(exportEndpointSlice))
	}, time.Second*60, time.Second*1)

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	gvr := apiextensionsv1.SchemeGroupVersion.WithResource("customresourcedefinitions")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		items := []unstructured.Unstructured{}

		for _, vw := range exportEndpointSlice.Status.APIExportEndpoints {
			vwClusterClient, err := kcpdynamic.NewForConfig(apiexportVWConfig(t, rawConfig, vw.URL))
			require.NoError(t, err)

			list, err := vwClusterClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("Error listing CustomResourceDefinitions on %s: %v", vw.URL, err)
			}

			if list != nil {
				items = append(items, list.Items...)
			}
		}

		return len(items) == 1, fmt.Sprintf("Unexpected number of CRDs found. Expected 1, found %d", len(items))
	}, wait.ForeverTestTimeout, time.Second*1)
}
