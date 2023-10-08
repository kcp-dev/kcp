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
	"strings"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingDeletion(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	providerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	require.NoError(t, err, "failed to construct crd cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerPath, providerPath)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
		},
	}

	framework.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Should have finalizer added in apibinding")
	require.Eventually(t, func() bool {
		apibinding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(apibinding.Finalizers) == 0 {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerPath)
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := consumerWorkspaceClient.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerPath, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumerPath)
	cowboyClient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumerPath).Cowboys("default")
	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("cowboy-1-%s", strings.ReplaceAll(consumerPath.String(), ":", "-")),
			Namespace: "default",
		},
	}
	_, err = cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumerPath)

	t.Logf("Create a cowboy CR with finalizer in consumer workspace %q", consumerPath)
	cowboyName := fmt.Sprintf("cowboy-2-%s", strings.ReplaceAll(consumerPath.String(), ":", "-"))
	cowboy = &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       cowboyName,
			Namespace:  "default",
			Finalizers: []string{"tenancy.kcp.io/test-finalizer"},
		},
	}
	_, err = cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumerPath)
	require.NoError(t, err, "error getting apibinding in consumer workspace %q", consumerPath)

	err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Delete(ctx, apiBinding.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("There should left 1 cowboy CR in delete state")
	require.Eventually(t, func() bool {
		cowboys, err := cowboyClient.List(ctx, metav1.ListOptions{})
		if err != nil {
			return false
		}

		if len(cowboys.Items) != 1 {
			return false
		}

		return !cowboys.Items[0].DeletionTimestamp.IsZero()
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("apibinding should have BindingResourceDeleteSuccess with false status")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	}, framework.IsNot(apisv1alpha1.BindingResourceDeleteSuccess))

	t.Logf("ensure resource does not have create verb when deleting")
	require.Eventually(t, func() bool {
		resources, err := consumerWorkspaceClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
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

	t.Logf("Create another cowboy CR in consumer workspace %q", consumerPath)
	cowboyDenied := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("cowboy-3-%s", consumerPath.String()),
			Namespace: "default",
		},
	}
	_, err = cowboyClient.Create(ctx, cowboyDenied, metav1.CreateOptions{})
	require.Equal(t, apierrors.IsMethodNotSupported(err), true)

	t.Logf("Clean finalizer to remove the cowboy")
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cowboy, err = cowboyClient.Get(ctx, cowboyName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		cowboy.Finalizers = []string{}
		_, err := cowboyClient.Update(ctx, cowboy, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "failed to update cowoby %s", cowboyName)

	t.Logf("apibinding should be deleted")
	require.Eventually(t, func() bool {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
