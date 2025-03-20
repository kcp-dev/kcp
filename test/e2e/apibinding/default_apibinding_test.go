/*
Copyright 2021 The KCP Authors.

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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestDefaultAPIBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("providerPath: %v", providerPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	_, err = kcpClusterClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "failed to list logical clusters")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	apiExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "",
						Resource: "configmaps",
					},
					ResourceSelector: []apisv1alpha1.ResourceSelector{
						{
							Name:      "test",
							Namespace: "test-namespace",
						},
					},
				},
			},
		},
	}
	apiExportCreated, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	workspaceType := tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-consumer-type",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultChildWorkspaceType: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: "universal",
				Path: "root",
			},
			Extend: tenancyv1alpha1.WorkspaceTypeExtension{
				With: []tenancyv1alpha1.WorkspaceTypeReference{
					{
						Name: "universal",
						Path: "root",
					},
				},
			},
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Export: apiExport.GetName(),
				},
			},
			DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
		},
	}

	_, err = kcpClusterClient.Cluster(providerPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, &workspaceType, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace type")

	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithType(providerPath, tenancyv1alpha1.WorkspaceTypeName(workspaceType.GetName())))
	t.Logf("consumerPath: %v", consumerPath)

	awaitAPIBinding := func(apiExport *apisv1alpha1.APIExport, group, resource string, expectedAppliedPermissionsClaims ...apisv1alpha1.PermissionClaim) {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("failed to list api bindings: %v", err)
			}
			for _, ab := range list.Items {
				if !strings.HasPrefix(ab.GetName(), apiExport.GetName()) {
					continue
				}
				hasBoundAPI := slices.ContainsFunc(ab.Status.BoundResources, func(br apisv1alpha1.BoundAPIResource) bool {
					return br.Group == group && br.Resource == resource
				})
				if !hasBoundAPI {
					continue
				}
				for _, epc := range expectedAppliedPermissionsClaims {
					hasAppliedPermissionClaim := slices.ContainsFunc(ab.Status.AppliedPermissionClaims, func(pc apisv1alpha1.PermissionClaim) bool {
						return epc.Group == pc.Group && epc.Resource == pc.Resource
					})
					if !hasAppliedPermissionClaim {
						return false, fmt.Sprintf("found api binding %q but missing permission claim %s/%s", ab.GetName(), epc.Group, epc.Resource)
					}
				}
				return true, fmt.Sprintf("found api binding %q", ab.GetName())
			}
			return false, ""
		}, wait.ForeverTestTimeout, time.Second*2, fmt.Sprintf("failed to wait for default api binding %q %q from export %q", group, resource, apiExport.GetName()))
	}

	awaitAPIBinding(
		apiExport, "wildwest.dev", "cowboys",
		apisv1alpha1.PermissionClaim{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    "",
				Resource: "configmaps",
			},
		},
	)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerPath)
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_tlsroutes.yaml", testFiles)
	require.NoError(t, err)

	currentAPIExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, apiExport.GetName(), metav1.GetOptions{})
	require.NoError(t, err)

	currentAPIExport.Spec.LatestResourceSchemas = append(apiExportCreated.Spec.LatestResourceSchemas, "latest.tlsroutes.gateway.networking.k8s.io")
	currentAPIExport.Spec.PermissionClaims = append(currentAPIExport.Spec.PermissionClaims, apisv1alpha1.PermissionClaim{
		GroupResource: apisv1alpha1.GroupResource{
			Group:    "",
			Resource: "secrets",
		},
		ResourceSelector: []apisv1alpha1.ResourceSelector{
			{
				Name:      "test",
				Namespace: "test-namespace",
			},
		},
	})

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Update(ctx, currentAPIExport, metav1.UpdateOptions{})
	require.NoError(t, err)

	awaitAPIBinding(
		apiExport, "gateway.networking.k8s.io", "tlsroutes",
		apisv1alpha1.PermissionClaim{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    "",
				Resource: "configmaps",
			},
		},
		apisv1alpha1.PermissionClaim{
			GroupResource: apisv1alpha1.GroupResource{
				Group:    "",
				Resource: "secrets",
			},
		},
	)
}
