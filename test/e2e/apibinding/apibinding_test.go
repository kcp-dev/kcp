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
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestAPIBinding(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)

	// These are the cluster name paths (i.e. /clusters/$org:$workspace) for our two workspaces.
	_, org, err := helper.ParseLogicalClusterName(orgClusterName)
	require.NoError(t, err)

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Creating source workspace in org lcluster %s", orgClusterName)
	orgKcpClient := kcpClients.Cluster(orgClusterName)
	sourceWorkspace := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "source-",
		},
	}
	sourceWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
	require.NoError(t, err, "error creating source workspace")
	require.Eventually(t, func() bool {
		ws, err := orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
		require.NoError(t, err, "error getting source workspace")
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for source workspace to be ready")

	server.Artifact(t, func() (runtime.Object, error) {
		return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
	})

	t.Logf("Creating target workspace in org lcluster %s", orgClusterName)
	targetWorkspace := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "target-",
		},
	}
	targetWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, targetWorkspace, metav1.CreateOptions{})
	require.NoError(t, err, "error creating target workspace")
	require.Eventually(t, func() bool {
		ws, err := orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, targetWorkspace.Name, metav1.GetOptions{})
		require.NoError(t, err, "error getting target workspace")
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for target workspace to be ready")

	server.Artifact(t, func() (runtime.Object, error) {
		return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, targetWorkspace.Name, metav1.GetOptions{})
	})

	var (
		sourceWorkspaceClusterName = org + ":" + sourceWorkspace.Name
		targetWorkspaceClusterName = org + ":" + targetWorkspace.Name
	)

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourceWorkspace.Name)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(sourceWorkspaceClusterName).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspaceClusterName), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(sourceWorkspaceClusterName).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetWorkspace.Name)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: sourceWorkspace.Name,
					ExportName:    cowboysAPIExport.Name,
				},
			},
		},
	}

	_, err = kcpClients.Cluster(targetWorkspaceClusterName).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Make sure %s API group does NOT show up in workspace %q group discovery", wildwest.GroupName, sourceWorkspace.Name)
	groups, err := kcpClients.Cluster(sourceWorkspaceClusterName).Discovery().ServerGroups()
	require.NoError(t, err, "error retrieving workspace %q group discovery", sourceWorkspace.Name)
	require.False(t, groupExists(groups, wildwest.GroupName),
		"should not have seen %s API group in workspace %q group discovery", wildwest.GroupName, sourceWorkspace.Name)

	t.Logf("Make sure %q API group shows up in workspace %q group discovery", tenancy.GroupName, sourceWorkspace.Name)
	err = wait.PollImmediateUntilWithContext(ctx, 100*time.Millisecond, func(c context.Context) (done bool, err error) {
		groups, err := kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving source workspace group discovery: %w", err)
		}
		if groupExists(groups, wildwest.GroupName) {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err)

	t.Logf("Make sure cowboys API resource shows up in workspace %q group version discovery", targetWorkspace.Name)
	resources, err := kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving workspace %q API discovery", targetWorkspace.Name)
	require.True(t, resourceExists(resources, "cowboys"), "workspace %q discovery is missing cowboys resource", targetWorkspace.Name)

	wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Make sure we can perform CRUD operations workspace %q for the bound API", targetWorkspace.Name)

	t.Logf("Make sure list shows nothing to start")
	targetCowboyClient := wildwestClusterClient.Cluster(targetWorkspaceClusterName).WildwestV1alpha1().Cowboys("default")
	cowboys, err := targetCowboyClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing cowboys inside workspace %q", targetWorkspace.Name)
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside workspace %q", targetWorkspace.Name)

	t.Logf("Create a cowboy CR in workspace %q", targetWorkspace.Name)
	targetWorkspaceCowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target-cowboy",
			Namespace: "default",
		},
	}
	_, err = targetCowboyClient.Create(ctx, targetWorkspaceCowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating targetWorkspaceCowboy in workspace %q", targetWorkspace.Name)

	t.Logf("Make sure there is 1 cowboy in workspace %q", targetWorkspace.Name)
	cowboys, err = targetCowboyClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing cowboys in workspace %q", targetWorkspace.Name)
	require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in workspace %q", targetWorkspace.Name)
	require.Equal(t, "target-cowboy", cowboys.Items[0].Name, "unexpected name for cowboy in workspace %q", targetWorkspace.Name)
}

func groupExists(list *metav1.APIGroupList, group string) bool {
	for _, g := range list.Groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

func resourceExists(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}
