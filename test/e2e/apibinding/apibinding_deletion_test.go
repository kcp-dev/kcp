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

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kcpdynamic "github.com/kcp-dev/apimachinery/pkg/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingDeletion(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewClusterDynamicClientForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClusterCfg := kcpclienthelper.SetCluster(rest.CopyConfig(cfg), serviceProviderWorkspace)
	serviceProviderClient, err := clientset.NewForConfig(serviceProviderClusterCfg)
	require.NoError(t, err)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
	_, err = kcpClusterClient.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, serviceProviderWorkspace), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, serviceProviderWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       serviceProviderWorkspace.String(),
					ExportName: "today-cowboys",
				},
			},
		},
	}

	_, err = kcpClusterClient.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Should have finalizer added in apibinding")
	require.Eventually(t, func() bool {
		apibinding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(apibinding.Finalizers) == 0 {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	consumerWorkspaceConfig := kcpclienthelper.SetCluster(rest.CopyConfig(cfg), consumerWorkspace)
	consumerWorkspaceClient, err := clientset.NewForConfig(consumerWorkspaceConfig)
	require.NoError(t, err)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
		groups, err := consumerWorkspaceClient.Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumerWorkspace)
	cowboyClusterClient := wildwestClusterClient.WildwestV1alpha1().Cowboys("default")
	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("cowboy-1-%s", consumerWorkspace.Base()),
			Namespace: "default",
		},
	}
	_, err = cowboyClusterClient.Create(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumerWorkspace)

	t.Logf("Create a cowboy CR with finalizer in consumer workspace %q", consumerWorkspace)
	cowboyName := fmt.Sprintf("cowboy-2-%s", consumerWorkspace.Base())
	cowboy = &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       cowboyName,
			Namespace:  "default",
			Finalizers: []string{"tenancy.kcp.dev/test-finalizer"},
		},
	}
	_, err = cowboyClusterClient.Create(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumerWorkspace)

	err = kcpClusterClient.ApisV1alpha1().APIBindings().Delete(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("There should left 1 cowboy CR in delete state")
	require.Eventually(t, func() bool {
		cowboys, err := cowboyClusterClient.List(logicalcluster.WithCluster(ctx, consumerWorkspace), metav1.ListOptions{})
		if err != nil {
			return false
		}

		if len(cowboys.Items) != 1 {
			return false
		}

		return !cowboys.Items[0].DeletionTimestamp.IsZero()
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("apibinding should have BindingResourceDeleteSuccess with false status")
	require.Eventually(t, func() bool {
		apibinding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		return conditions.IsFalse(apibinding, apisv1alpha1.BindingResourceDeleteSuccess)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("ensure resource does not have create verb when deleting")
	require.Eventually(t, func() bool {
		resources, err := consumerWorkspaceClient.Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		if err != nil {
			return false
		}

		for _, r := range resources.APIResources {
			if r.Name != "cowboys" {
				continue
			}

			for _, v := range r.Verbs {
				if v == "create" {
					return false
				}
			}
		}

		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Create another cowboy CR in consumer workspace %q", consumerWorkspace)
	cowboyDenied := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("cowboy-3-%s", consumerWorkspace.Base()),
			Namespace: "default",
		},
	}
	_, err = cowboyClusterClient.Create(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboyDenied, metav1.CreateOptions{})
	require.Equal(t, apierrors.IsMethodNotSupported(err), true)

	t.Logf("Clean finalizer to remove the cowboy")
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cowboy, err = cowboyClusterClient.Get(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboyName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		cowboy.Finalizers = []string{}
		_, err := cowboyClusterClient.Update(logicalcluster.WithCluster(ctx, consumerWorkspace), cowboy, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "failed to update cowoby %s", cowboyName)

	t.Logf("apibinding should be deleted")
	require.Eventually(t, func() bool {
		_, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
