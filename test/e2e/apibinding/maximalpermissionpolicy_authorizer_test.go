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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMaximalPermissionPolicyAuthorizerSystemGroupProtection(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for user-1")

	rootKcpClusterClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)

	t.Logf("Creating workspace")
	orgClusterName := framework.NewOrganizationFixture(t, server, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{
		Name: "root",
	}))

	t.Logf("Giving user-1 admin access")
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName.Path(), []string{"user-1"}, nil, true)

	type Test struct {
		name string
		test func(t *testing.T)
	}

	for _, test := range []Test{
		{
			name: "APIBinding resources",
			test: func(t *testing.T) {
				t.Parallel()

				t.Logf("Creating a ClusterWorkspaceType as user-1")
				userKcpClusterClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", server.BaseConfig(t)))
				require.NoError(t, err, "failed to construct kcp cluster client for user-1")
				framework.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
					_, err = userKcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
						ObjectMeta: metav1.ObjectMeta{
							Name: "test",
						},
					}, metav1.CreateOptions{})
					if err != nil {
						return false, err.Error()
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Logf("Trying to change the status as user-1 and that should fail")
				patch := []byte(`{"status":{"Initializers":["foo"]}}`)
				wc, err := userKcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaceTypes().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, wc))

				t.Logf("Double check to change status as admin, which should work")
				_, err = kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaceTypes().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch status as admin")
			},
		},
		{
			name: "System CRDs",
			test: func(t *testing.T) {
				t.Parallel()

				t.Logf("Creating a APIExport as user-1")
				userKcpClusterClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", server.BaseConfig(t)))
				require.NoError(t, err, "failed to construct kcp cluster client for user-1")
				framework.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
					_, err := userKcpClusterClient.Cluster(orgClusterName.Path()).ApisV1alpha1().APIExports().Create(ctx, &apisv1alpha1.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Name: "test",
						},
					}, metav1.CreateOptions{})
					if err != nil {
						return false, err.Error()
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Logf("Trying to change the status as user-1 and that should fail")
				patch := []byte(`{"status":{"identityHash":"4711"}}`)
				export, err := userKcpClusterClient.Cluster(orgClusterName.Path()).ApisV1alpha1().APIExports().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, export))

				t.Logf("Double check to change status as system:master, which should work") // system CRDs even need system:master, hence we need the root shard client
				_, err = rootKcpClusterClient.Cluster(orgClusterName.Path()).ApisV1alpha1().APIExports().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch status as admin")
			},
		},
	} {
		t.Run(test.name, test.test)
	}
}

func TestMaximalPermissionPolicyAuthorizer(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	rbacServiceProviderClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	serviceProvider2Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	consumer1ClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	consumer2ClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	user3KcpClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClusterNames := []logicalcluster.Name{rbacServiceProviderClusterName, serviceProvider2Workspace}
	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, orgClusterName.Path(), []string{"user-1", "user-2", "user-3"}, nil, false)

	// Set up service provider workspace.
	for _, serviceProviderClusterName := range serviceProviderClusterNames {
		setUpServiceProvider(ctx, dynamicClients, kcpClusterClient, kubeClusterClient, serviceProviderClusterName.Path(), rbacServiceProviderClusterName.Path(), cfg, t)
	}

	bindConsumerToProvider := func(consumerWorkspace logicalcluster.Path, providerClusterName logicalcluster.Name) {
		t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumer1ClusterName, rbacServiceProviderClusterName)
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: providerClusterName.Path().String(),
						Name: "today-cowboys",
					},
				},
			},
		}

		// create API bindings in consumerWorkspace as user-3 with only bind permissions in serviceProviderWorkspace but not general access.
		require.Eventuallyf(t, func() bool {
			_, err = user3KcpClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-3 to bind cowboys in %q", consumerWorkspace)

		consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
		err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
			groups, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
			if err != nil {
				return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
			}
			return groupExists(groups, wildwest.GroupName), nil
		})
		require.NoError(t, err)
		t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
		resources, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
		require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
	}

	m := map[logicalcluster.Name]logicalcluster.Name{
		rbacServiceProviderClusterName: consumer1ClusterName,
		serviceProvider2Workspace:      consumer2ClusterName,
	}
	for serviceProvider, consumer := range m {
		t.Logf("Set up user-1 and user-3 as admin for the consumer workspace %q", consumer)
		framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, consumer.Path(), []string{"user-1", "user-3"}, nil, true)
		bindConsumerToProvider(consumer.Path(), serviceProvider)
		wildwestClusterClient, err := wildwestclientset.NewForConfig(framework.UserConfig("user-1", rest.CopyConfig(cfg)))
		cowboyclient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumer.Path()).Cowboys("default")
		require.NoError(t, err)
		testCRUDOperations(ctx, t, consumer.Path(), wildwestClusterClient)
		t.Logf("Make sure there is 1 cowboy in consumer workspace %q", consumer)
		cowboys, err := cowboyclient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
		require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in consumer workspace %q", consumer)
		if serviceProvider == rbacServiceProviderClusterName {
			t.Logf("Make sure that the status of cowboy can not be updated in workspace %q", consumer)
			framework.Eventually(t, func() (bool, string) {
				_, err = cowboyclient.UpdateStatus(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
				if err == nil {
					return false, "error"
				}
				return true, err.Error()
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			// in consumer workspace 1 we will create a RBAC for user 2 such that they can only get/list.
			t.Logf("Install RBAC in consumer workspace %q for user 2", consumer)
			clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-get-list", "user-2", "User", wildwest.GroupName, "cowboys", "", []string{"get", "list"})
			_, err = kubeClusterClient.Cluster(consumer.Path()).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = kubeClusterClient.Cluster(consumer.Path()).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, consumer.Path(), []string{"user-2"}, nil, false)
			user2Client, err := wildwestclientset.NewForConfig(framework.UserConfig("user-2", rest.CopyConfig(cfg)))
			require.NoError(t, err)

			t.Logf("Make sure user 2 can list cowboys in consumer workspace %q", consumer)

			// This is needed to make sure the RBAC is updated in the informers
			require.Eventually(t, func() bool {
				_, err := user2Client.Cluster(consumer.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-2 to list cowboys")

			cowboy2 := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-2-cowboy",
					Namespace: "default",
				},
			}
			t.Logf("Make sure user 2 can not create cowboy resources in consumer workspace %q", consumer)
			_, err = user2Client.Cluster(consumer.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy2, metav1.CreateOptions{})
			require.Error(t, err)
		} else {
			t.Logf("Make sure that the status of cowboy can be updated in workspace %q", consumer)
			_, err = cowboyclient.Update(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
			require.NoError(t, err, "expected error updating status of cowboys")
		}
	}

	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: "root:not-existent",
					Name: "today-cowboys",
				},
			},
		},
	}

	_, err = user3KcpClient.Cluster(consumer1ClusterName.Path()).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.ErrorContains(t, err, `no permission to bind to export root:not-existent:today-cowboys`)
}

func createClusterRoleAndBindings(name, subjectName, subjectKind string, apiGroup, resource, resourceName string, verbs []string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     verbs,
				APIGroups: []string{apiGroup},
				Resources: []string{resource},
			},
		},
	}

	if resourceName != "" {
		clusterRole.Rules[0].ResourceNames = []string{resourceName}
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
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

func setUpServiceProvider(ctx context.Context, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, kubeClusterClient kcpkubernetesclientset.ClusterInterface, serviceProviderWorkspace, rbacServiceProvider logicalcluster.Path, cfg *rest.Config, t *testing.T) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
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
	if serviceProviderWorkspace == rbacServiceProvider {
		cowboysAPIExport.Spec.MaximalPermissionPolicy = &apisv1alpha1.MaximalPermissionPolicy{Local: &apisv1alpha1.LocalAPIExportPolicy{}}
		// install RBAC that allows create/list/get/update/watch on cowboys for system:authenticated
		t.Logf("Install RBAC for API Export in serviceProvider1")
		clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-systemauth", "apis.kcp.dev:binding:system:authenticated", "Group", wildwest.GroupName, "cowboys", "", []string{rbacv1.VerbAll})
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// permit user-3 to be able to bind the api export
	clusterRole, clusterRoleBinding := createClusterRoleAndBindings("user-3-binding", "user-3", "User", apisv1alpha1.SchemeGroupVersion.Group, "apiexports", "today-cowboys", []string{"bind"})
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)
}

func testCRUDOperations(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface) {
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

func toYAML(t *testing.T, binding interface{}) string {
	bs, err := yaml.Marshal(binding)
	require.NoError(t, err)
	return string(bs)
}
