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

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMaximalPermissionPolicyAuthorizerSystemGroupProtection(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for user-1")

	rootKcpClusterClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)

	t.Logf("Creating workspace")
	orgPath, _ := framework.NewOrganizationFixture(t, server, kcptesting.WithRootShard()) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.

	t.Logf("Giving user-1 admin access")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, orgPath, []string{"user-1"}, nil, true)

	type Test struct {
		name string
		test func(t *testing.T)
	}

	for _, testCase := range []Test{
		{
			name: "APIBinding resources",
			test: func(t *testing.T) {
				t.Logf("Creating a WorkspaceType as user-1")
				userKcpClusterClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", server.BaseConfig(t)))
				require.NoError(t, err, "failed to construct kcp cluster client for user-1")
				kcptestinghelpers.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
					_, err = userKcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
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
				wc, err := userKcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, wc))

				t.Logf("Double check to change status as admin, which should work")
				_, err = kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch status as admin")
			},
		},
		{
			name: "System CRDs",
			test: func(t *testing.T) {
				t.Logf("Creating a APIExport as user-1")
				userKcpClusterClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", server.BaseConfig(t)))
				require.NoError(t, err, "failed to construct kcp cluster client for user-1")
				kcptestinghelpers.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
					_, err := userKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
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
				export, err := userKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, export))

				t.Logf("Double check to change status as system:master, which should work") // system CRDs even need system:master, hence we need the root shard client
				_, err = rootKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Patch(ctx, "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch status as admin")
			},
		},
	} {
		test := testCase
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.test(t)
		})
	}
}

func TestMaximalPermissionPolicyAuthorizer(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	rbacServiceProviderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	serviceProvider2Workspace, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	user3KcpClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-3", rest.CopyConfig(cfg)))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClusterNames := []logicalcluster.Path{rbacServiceProviderPath, serviceProvider2Workspace}
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, orgPath, []string{"user-1", "user-2", "user-3"}, nil, false)

	// Set up service provider workspace.
	for _, serviceProviderPath := range serviceProviderClusterNames {
		setUpServiceProvider(ctx, t, dynamicClients, kcpClusterClient, kubeClusterClient, serviceProviderPath, rbacServiceProviderPath, cfg)
	}

	bindConsumerToProvider := func(consumerWorkspace logicalcluster.Path, providerClusterName logicalcluster.Path) {
		t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumer1Path, rbacServiceProviderPath)
		apiBinding := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: providerClusterName.String(),
						Name: "today-cowboys",
					},
				},
			},
		}

		// create API bindings in consumerWorkspace as user-3 with only bind permissions in serviceProviderWorkspace but not general access.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err = user3KcpClient.Cluster(consumerWorkspace).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-3 to bind cowboys in %q", consumerWorkspace)

		consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
		err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
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

	m := map[logicalcluster.Path]logicalcluster.Path{
		rbacServiceProviderPath:   consumer1Path,
		serviceProvider2Workspace: consumer2Path,
	}
	for serviceProvider, consumer := range m {
		t.Logf("Set up user-1 and user-3 as admin for the consumer workspace %q", consumer)
		framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, consumer, []string{"user-1", "user-3"}, nil, true)
		bindConsumerToProvider(consumer, serviceProvider)
		wildwestClusterClient, err := wildwestclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", rest.CopyConfig(cfg)))
		cowboyclient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumer).Cowboys("default")
		require.NoError(t, err)
		testCRUDOperations(ctx, t, consumer, wildwestClusterClient)
		t.Logf("Make sure there is 1 cowboy in consumer workspace %q", consumer)
		cowboys, err := cowboyclient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
		require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in consumer workspace %q", consumer)
		if serviceProvider == rbacServiceProviderPath {
			t.Logf("Make sure that the status of cowboy can not be updated in workspace %q", consumer)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = cowboyclient.UpdateStatus(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
				if err == nil {
					return false, "error"
				}
				return true, err.Error()
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			// in consumer workspace 1 we will create a RBAC for user 2 such that they can only get/list.
			t.Logf("Install RBAC in consumer workspace %q for user 2", consumer)
			clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-get-list", "user-2", "User", wildwest.GroupName, "cowboys", "", []string{"get", "list"})
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, consumer, []string{"user-2"}, nil, false)
			user2Client, err := wildwestclientset.NewForConfig(framework.StaticTokenUserConfig("user-2", rest.CopyConfig(cfg)))
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

			// Create user-2-cowboy for the admin to delete after the APIExport deletion.
			_, err = cowboyclient.Create(ctx, cowboy2, metav1.CreateOptions{})
			require.NoError(t, err)

			// Create another cowboy that will be deleted upon the deletion of the APIBinding.
			cowboy3 := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cowboy-3",
					Namespace: "default",
				},
			}
			_, err = cowboyclient.Create(ctx, cowboy3, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("User 2 can list cowboys in consumer workspace %q before deleting APIExport", consumer)
			user2Cowboys, err := user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			require.Equal(t, 3, len(user2Cowboys.Items), "expected 3 cowboys in consumer")

			t.Logf("User 2 gets errors trying to delete an existing cowboy in consumer workspace %q", consumer)
			err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(ctx, cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.Error(t, err)

			t.Logf("Delete APIExport in provider workspace %q", rbacServiceProviderPath)
			err = kcpClusterClient.Cluster(rbacServiceProviderPath).ApisV1alpha2().APIExports().Delete(ctx, "today-cowboys", metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("Wait for APIExport deletion in provider workspace %q", rbacServiceProviderPath)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(rbacServiceProviderPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, ""
				}
				if err != nil {
					return false, fmt.Sprintf("error getting APIExport: %v", err)
				}
				return false, "APIExport still exists"
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("User 2 can list cowboys in consumer workspace %q despite deleted APIExport", consumer)
			_, err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
			require.NoError(t, err)

			// Due to RBAC - _not_ due to the deleted APIExport. This check is just to ensure the RBAC is not affected by the APIExport deletion.
			t.Logf("User 2 should get errors trying to delete an existing cowboy in consumer workspace %q", consumer)
			err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(ctx, cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.Error(t, err)

			t.Logf("Admin can list the cowboys in consumer workspace %q", consumer)
			cowboysAfterDelete, err := wildwestClusterClient.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
			require.Equal(t, 3, len(cowboysAfterDelete.Items), "expected 3 cowboy in consumer")

			t.Logf("Admin can delete an existing cowboy in consumer workspace %q", consumer)
			err = wildwestClusterClient.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(ctx, cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("APIBinding deletion does not error")
			err = kcpClusterClient.Cluster(consumer).ApisV1alpha2().APIBindings().Delete(ctx, "cowboys", metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("Wait for APIBinding deletion in consumer workspace %q", consumer)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(consumer).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, ""
				}
				if err != nil {
					return false, fmt.Sprintf("error getting APIBinding: %v", err)
				}
				return false, "APIBinding still exists"
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("The CRDs are deleted in consumer workspace %q", consumer)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				apiResourceList, err := kubeClusterClient.Cluster(consumer).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
				if apierrors.IsNotFound(err) {
					return true, ""
				}
				if err != nil {
					return false, fmt.Sprintf("error listing resources: %v", err)
				}
				if len(apiResourceList.APIResources) == 0 {
					return true, "resources are deleted"
				}
				return false, fmt.Sprintf("expected no resources, got: %v", apiResourceList.APIResources)
			}, wait.ForeverTestTimeout, time.Millisecond*100)
		} else {
			t.Logf("Make sure that the status of cowboy can be updated in workspace %q", consumer)
			_, err = cowboyclient.Update(ctx, &cowboys.Items[0], metav1.UpdateOptions{})
			require.NoError(t, err, "expected error updating status of cowboys")
		}
	}

	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: "root:not-existent",
					Name: "today-cowboys",
				},
			},
		},
	}

	_, err = user3KcpClient.Cluster(consumer1Path).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
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

func setUpServiceProvider(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, kubeClusterClient kcpkubernetesclientset.ClusterInterface, serviceProviderWorkspace, rbacServiceProvider logicalcluster.Path, cfg *rest.Config) {
	t.Helper()
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	if serviceProviderWorkspace == rbacServiceProvider {
		cowboysAPIExport.Spec.MaximalPermissionPolicy = &apisv1alpha2.MaximalPermissionPolicy{Local: &apisv1alpha2.LocalAPIExportPolicy{}}
		// install RBAC that allows create/list/get/update/watch on cowboys for system:authenticated
		t.Logf("Install RBAC for API Export in serviceProvider1")
		clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-systemauth", "apis.kcp.io:binding:system:authenticated", "Group", wildwest.GroupName, "cowboys", "", []string{rbacv1.VerbAll})
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// permit user-3 to be able to bind the api export
	clusterRole, clusterRoleBinding := createClusterRoleAndBindings("user-3-binding", "user-3", "User", apisv1alpha2.SchemeGroupVersion.Group, "apiexports", "today-cowboys", []string{"bind"})
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)
}

func testCRUDOperations(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface) {
	t.Helper()
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

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to create cowboy in consumer workspace %q", consumer1Workspace)
}

func toYAML(t *testing.T, binding interface{}) string {
	t.Helper()
	bs, err := yaml.Marshal(binding)
	require.NoError(t, err)
	return string(bs)
}
