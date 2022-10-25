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

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestClusterWorkspaceTypeAPIBindingInitialization(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	cowboysProvider := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("cowboys-provider"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclient.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	cowboysProviderConfig := kcpclienthelper.SetCluster(rest.CopyConfig(cfg), cowboysProvider)
	cowboysProviderKCPClient, err := kcpclient.NewForConfig(cowboysProviderConfig)
	require.NoError(t, err, "error creating cowboys provider kcp client")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", cowboysProvider)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cowboysProviderKCPClient.Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(cowboysProvider), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
				},
			},
		},
	}
	cowboysAPIExport, err = kcpClusterClient.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, cowboysProvider), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	universal := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("universal"))

	cwtParent1 := &tenancyv1alpha1.ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent1",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:       logicalcluster.From(cowboysAPIExport).String(),
					ExportName: cowboysAPIExport.Name,
				},
				{
					Path:       "root",
					ExportName: "scheduling.kcp.dev",
				},
			},
		},
	}

	cwtParent2 := &tenancyv1alpha1.ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "parent2",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:       "root",
					ExportName: "workload.kcp.dev",
				},
			},
		},
	}

	cwt := &tenancyv1alpha1.ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:       "root",
					ExportName: "shards.tenancy.kcp.dev",
				},
			},
			Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
				With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
					{
						Name: "parent1",
						Path: universal.String(),
					},
					{
						Name: "parent2",
						Path: universal.String(),
					},
				},
			},
		},
	}

	t.Logf("Creating ClusterWorkspaceType parent1")
	_, err = kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(logicalcluster.WithCluster(ctx, universal), cwtParent1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt parent1")

	t.Logf("Creating ClusterWorkspaceType parent2")
	_, err = kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(logicalcluster.WithCluster(ctx, universal), cwtParent2, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt parent2")

	t.Logf("Creating ClusterWorkspaceType test")
	_, err = kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceTypes().Create(logicalcluster.WithCluster(ctx, universal), cwt, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cwt test")

	// This will create and wait for ready, which only happens if the APIBinding initialization is working correctly
	_ = framework.NewWorkspaceFixture(t, server, universal, framework.WithType(universal, "test"), framework.WithName("init"))
}
