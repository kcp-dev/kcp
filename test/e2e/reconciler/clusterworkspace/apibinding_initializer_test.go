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

package clusterworkspace

import (
	"context"
	"testing"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceTypesAPIBindingInitialization(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	org := framework.NewOrganizationFixtureObject(t, server)
	orgPath := tenancyv1alpha1.RootCluster.Path().Join(org.Name)
	cowboysProviderWorkspace := framework.NewWorkspaceFixtureObject(t, server, orgPath, framework.WithName("cowboys-provider"))
	cowboysProviderPath := orgPath.Join(cowboysProviderWorkspace.Name)

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

	universal := framework.NewWorkspaceFixtureObject(t, server, orgPath, framework.WithName("universal"))
	universalPath := orgPath.Join(universal.Name)

	cwtParent1 := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent1",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   cowboysProviderPath.String(),
					Export: cowboysAPIExport.Name,
				},
				{
					Path:   "root",
					Export: "scheduling.kcp.dev",
				},
			},
		},
	}

	cwtParent2 := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent2",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   "root",
					Export: "workload.kcp.dev",
				},
			},
		},
	}

	cwt := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   "root",
					Export: "shards.tenancy.kcp.dev",
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
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, cwtParent1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt parent1")

	t.Logf("Creating WorkspaceType parent2")
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, cwtParent2, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt parent2")

	t.Logf("Creating WorkspaceType test")
	_, err = kcpClusterClient.Cluster(universalPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, cwt, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt test")

	// This will create and wait for ready, which only happens if the APIBinding initialization is working correctly
	_ = framework.NewWorkspaceFixture(t, server, universalPath, framework.WithType(universalPath, "test"), framework.WithName("init"))
}
