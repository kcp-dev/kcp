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

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingAuthorizer(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	rbacServiceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	serviceProvider2Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumer1Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumer2Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderWorkspaces := []logicalcluster.Name{rbacServiceProviderWorkspace, serviceProvider2Workspace}
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName, []string{"user-1"}, nil, []string{"member"})
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName, []string{"user-2"}, nil, []string{"access"})

	// Set up service provider workspace.
	for _, serviceProviderWorkspace := range serviceProviderWorkspaces {
		setUpServiceProvider(ctx, dynamicClients, kcpClients, kubeClusterClient, serviceProviderWorkspace, rbacServiceProviderWorkspace, t)
	}

	bindConsumerToProvider := func(consumerWorkspace, providerWorkspace logicalcluster.Name) {
		t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumer1Workspace, rbacServiceProviderWorkspace)
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.ExportReference{
					Workspace: &apisv1alpha1.WorkspaceExportReference{
						Path:       providerWorkspace.String(),
						ExportName: "today-cowboys",
					},
				},
			},
		}

		_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
		err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
			groups, err := kcpClients.Cluster(consumerWorkspace).Discovery().ServerGroups()
			if err != nil {
				return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
			}
			return groupExists(groups, wildwest.GroupName), nil
		})
		require.NoError(t, err)
		t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
		resources, err := kcpClients.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
		require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
	}

	m := map[logicalcluster.Name]logicalcluster.Name{
		rbacServiceProviderWorkspace: consumer1Workspace,
		serviceProvider2Workspace:    consumer2Workspace,
	}
	for serviceProvider, consumer := range m {
		bindConsumerToProvider(consumer, serviceProvider)
		t.Logf("Set up user 1 as admin for the consumer workspace %q", consumer)
		framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, consumer, []string{"user-1"}, nil, []string{"admin"})
		wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(framework.UserConfig("user-1", cfg))
		cowboyClient := wildwestClusterClient.Cluster(consumer).WildwestV1alpha1().Cowboys("default")
		require.NoError(t, err)
		testCRUDOperations(ctx, t, consumer, wildwestClusterClient)
		t.Logf("Make sure there is 1 cowboy in consumer workspace %q", consumer)
		cowboys, err := cowboyClient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
		require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in consumer workspace %q", consumer)
		if serviceProvider == rbacServiceProviderWorkspace {
			t.Logf("Make sure that the status of cowboy can not be updated in workspace %q", consumer)
			_, err = cowboyClient.UpdateStatus(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
			require.Error(t, err, "expected error updating status of cowboys")

			// in consumer workspace 1 we will create a RBAC for user 2 such that they can only get/list.
			t.Logf("Install RBAC in consumer workspace %q for user 2", consumer)
			clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-get-list", "user-2", "User", []string{"get", "list"})
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, consumer, []string{"user-2"}, nil, []string{"access"})
			user2Client, err := wildwestclientset.NewClusterForConfig(framework.UserConfig("user-2", cfg))
			require.NoError(t, err)

			t.Logf("Make sure user 2 can list cowboys in consumer workspace %q", consumer)

			// This is needed to make sure the RBAC is updated in the informers
			require.Eventually(t, func() bool {
				_, err := user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-2 to list cowboys")

			cowboy2 := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-2-cowboy",
					Namespace: "default",
				},
			}
			t.Logf("Make sure user 2 can not create cowboy resources in consumer workspace %q", consumer)
			_, err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy2, metav1.CreateOptions{})
			require.Error(t, err)
		} else {
			t.Logf("Make sure that the status of cowboy can be updated in workspace %q", consumer)
			_, err = cowboyClient.Update(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
			require.NoError(t, err, "expected error updating status of cowboys")
		}
	}
}

func createClusterRoleAndBindings(name, subjectName, subjectKind string, verbs []string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     verbs,
				APIGroups: []string{wildwest.GroupName},
				Resources: []string{"cowboys"},
			},
		},
	}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-user2-get-list",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: subjectKind,
				Name: subjectName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     name,
		},
	}
	return clusterRole, clusterRoleBinding
}

func setUpServiceProvider(ctx context.Context, dynamicClients *dynamic.Cluster, kcpClients *clientset.Cluster, kubeClusterClient *kubernetes.Cluster, serviceProviderWorkspace, rbacServiceProvider logicalcluster.Name, t *testing.T) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(serviceProviderWorkspace).Discovery()))
	err := helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(serviceProviderWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
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
	if serviceProviderWorkspace == rbacServiceProvider {
		cowboysAPIExport.Spec.MaximalPermissionPolicy = &apisv1alpha1.APIExportPolicy{Local: &apisv1alpha1.LocalAPIExportPolicy{}}
		//install RBAC that allows 	create/list/get/update/watch on cowboys for system:authenticated
		t.Logf("Install RBAC for API Export in serviceProvider1")
		clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-systemauth", "system:authenticated", "Group", []string{rbacv1.VerbAll})
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func testCRUDOperations(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Name, wildwestClusterClient *wildwestclientset.Cluster) {
	t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumer1Workspace)

	t.Logf("Make sure list shows nothing to start")
	cowboyClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	var cowboys *wildwestv1alpha1.CowboyList
	// Adding a poll here to wait for the user's to get access via RBAC informer updates.

	require.Eventually(t, func() bool {
		var err error
		cowboys, err = cowboyClient.List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list ")
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside consumer workspace %q", consumer1Workspace)

	t.Logf("Create a cowboy CR in consumer workspace %q", consumer1Workspace)
	cowboyName := fmt.Sprintf("cowboy-%s", consumer1Workspace.Base())
	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cowboyName,
			Namespace: "default",
		},
	}
	_, err := cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumer1Workspace)

}
