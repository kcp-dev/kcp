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
	"context"
	"fmt"
	"testing"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Test that service provider can access logical cluster object from within the
// consumers cluster when consumer binds to the provider's APIExport.
func TestAPIBindingLogicalCluster(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	providerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("providerPath: %v", providerPath)
	t.Logf("consumerPath: %v", consumerPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	_, err = kcpClusterClient.Cluster(providerPath).CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "failed to list logical clusters")

	exportName := "logical-clusters"
	apiExport := apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha1.APIExportSpec{
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "core.kcp.io",
						Resource: "logicalclusters",
					},
					All: true,
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, &apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api export")

	// validate the valid claims condition occurs
	t.Logf("validate that the permission claim's conditions true")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	apiBinding := apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: exportName,
				},
			},
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Group:    "core.kcp.io",
							Resource: "logicalclusters",
						},
						All: true,
					},
					State: apisv1alpha1.ClaimAccepted,
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, &apiBinding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api binding")

	t.Logf("Validate that the permission claims are valid")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.PermissionClaimsValid), "unable to see valid claims")

	export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	require.NoError(t, err)

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	gvr := corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters")

	framework.Eventually(t, func() (bool, string) {
		items := []unstructured.Unstructured{}

		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		for _, vw := range export.Status.VirtualWorkspaces {
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

	server := framework.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	providerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("providerPath: %v", providerPath)
	t.Logf("consumerPath: %v", consumerPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

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
	apiExport := apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha1.APIExportSpec{
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "apiextensions.k8s.io",
						Resource: "customresourcedefinitions",
					},
					All: true,
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, &apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api export")

	// validate the valid claims condition occurs
	t.Logf("validate that the permission claim's conditions true")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	apiBinding := apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: exportName,
				},
			},
			PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Group:    "apiextensions.k8s.io",
							Resource: "customresourcedefinitions",
						},
						All: true,
					},
					State: apisv1alpha1.ClaimAccepted,
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, &apiBinding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create api binding")

	t.Logf("Validate that the permission claims are valid")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, exportName, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.PermissionClaimsValid), "unable to see valid claims")

	export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	require.NoError(t, err)

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	gvr := apiextensionsv1.SchemeGroupVersion.WithResource("customresourcedefinitions")

	framework.Eventually(t, func() (bool, string) {
		items := []unstructured.Unstructured{}

		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		for _, vw := range export.Status.VirtualWorkspaces {
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
