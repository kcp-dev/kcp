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

package workspace

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceTypesAPIBindingInitialization(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	cowboysProviderPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("cowboys-provider"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	cowboysProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating cowboys provider kcp client")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", cowboysProviderPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cowboysProviderKCPClient.Cluster(cowboysProviderPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	// Create rolebinding to allow bind to APIExport of coboys provider. Else nobody can bind to it.
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "clusterrole_cowboys.yaml", testFiles)
	require.NoError(t, err)

	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "clusterrolebinding_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create today-cowboys APIExport")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Resource: "configmaps",
					},
					All: true,
				},
			},
		},
	}
	cowboysAPIExport, err = kcpClusterClient.Cluster(cowboysProviderPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	universalPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("universal"))

	wtParent1 := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent1",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   cowboysProviderPath.String(),
					Export: cowboysAPIExport.Name,
				},
			},
		},
	}

	wtParent2 := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent2",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   "root",
					Export: "topology.kcp.io",
				},
			},
		},
	}

	wt := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   "root",
					Export: "tenancy.kcp.io",
				},
			},
			Extend: tenancyv1alpha1.WorkspaceTypeExtension{
				With: []tenancyv1alpha1.WorkspaceTypeReference{
					{
						Name: "parent1",
						Path: universalPath.String(),
					},
					{
						Name: "parent2",
						Path: universalPath.String(),
					},
				},
			},
		},
	}

	t.Logf("Creating WorkspaceType parent1")
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wtParent1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating wt parent1")

	t.Logf("Creating WorkspaceType parent2")
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wtParent2, metav1.CreateOptions{})
	require.NoError(t, err, "error creating wt parent2")

	t.Logf("Creating WorkspaceType test")
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wt, metav1.CreateOptions{})
	require.NoError(t, err, "error creating wt test")

	// This will create and wait for ready, which only happens if the APIBinding initialization is working correctly
	_, _ = framework.NewWorkspaceFixture(t, server, universalPath, framework.WithType(universalPath, "test"), framework.WithName("init"))
}

func TestWorkspaceTypesAPIBindingWithCustomPermissions(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	cowboysProviderPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("cowboys-provider"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	cowboysProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating cowboys provider kcp client")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", cowboysProviderPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cowboysProviderKCPClient.Cluster(cowboysProviderPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	// Create rolebinding to allow bind to APIExport of coboys provider. Else nobody can bind to it.
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "clusterrole_cowboys.yaml", testFiles)
	require.NoError(t, err)

	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProviderPath), mapper, nil, "clusterrolebinding_cowboys_custom.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create today-cowboys APIExport")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Resource: "configmaps",
					},
					All: true,
				},
			},
		},
	}
	cowboysAPIExport, err = kcpClusterClient.Cluster(cowboysProviderPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	universalPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("universal"))

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	// create a user with custom permissions
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, universalPath, []string{"user"}, []string{"private-cowboys"}, true)
	userClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user", rest.CopyConfig(cfg)))
	require.NoError(t, err)

	framework.Eventually(t, func() (bool, string) {
		_, err := userClient.Cluster(universalPath).ApisV1alpha1().APIBindings().Create(ctx, &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "custom-binding",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: cowboysProviderPath.String(),
						Name: cowboysAPIExport.Name,
					},
				},
			},
		}, metav1.CreateOptions{})

		return err == nil, fmt.Sprintf("failed to create APIBinding with custom permissions: %v", err)
	}, wait.ForeverTestTimeout, time.Second*1)
}
