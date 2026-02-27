/*
Copyright 2022 The kcp Authors.

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

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMaximalPermissionPolicyAuthorizerSystemGroupProtection(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for user-1")

	rootKcpClusterClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)

	t.Logf("Creating workspace")
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithRootShard(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	t.Logf("Giving user-1 admin access")
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, orgPath, []string{"user-1"}, nil, true)

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
					_, err = userKcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), &tenancyv1alpha1.WorkspaceType{
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
				wc, err := userKcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Patch(t.Context(), "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, wc))

				t.Logf("Double check to change status as admin, which should work")
				_, err = kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().WorkspaceTypes().Patch(t.Context(), "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
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
					_, err := userKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
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
				export, err := userKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Patch(t.Context(), "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
				require.Error(t, err, "should have failed to patch status as user-1:\n%s", toYAML(t, export))

				t.Logf("Double check to change status as system:master, which should work") // system CRDs even need system:master, hence we need the root shard client
				_, err = rootKcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Patch(t.Context(), "test", types.MergePatchType, patch, metav1.PatchOptions{}, "status")
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

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
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
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, orgPath, []string{"user-1", "user-2", "user-3"}, nil, false)

	// Set up service provider workspace.
	for _, serviceProviderPath := range serviceProviderClusterNames {
		setUpServiceProvider(t, dynamicClients, kcpClusterClient, kubeClusterClient, serviceProviderPath, rbacServiceProviderPath, cfg)
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
			_, err = user3KcpClient.Cluster(consumerWorkspace).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-3 to bind cowboys in %q", consumerWorkspace)

		consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
		err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
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
		framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumer, []string{"user-1", "user-3"}, nil, true)
		bindConsumerToProvider(consumer, serviceProvider)
		wildwestClusterClient, err := wildwestclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", rest.CopyConfig(cfg)))
		cowboyclient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumer).Cowboys("default")
		require.NoError(t, err)
		testCRUDOperations(t, consumer, wildwestClusterClient)
		t.Logf("Make sure there is 1 cowboy in consumer workspace %q", consumer)
		cowboys, err := cowboyclient.List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
		require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in consumer workspace %q", consumer)
		if serviceProvider == rbacServiceProviderPath {
			t.Logf("Make sure that the status of cowboy can not be updated in workspace %q", consumer)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = cowboyclient.UpdateStatus(t.Context(), &cowboys.Items[0], metav1.UpdateOptions{})
				if err == nil {
					return false, "error"
				}
				return true, err.Error()
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			// in consumer workspace 1 we will create a RBAC for user 2 such that they can only get/list.
			t.Logf("Install RBAC in consumer workspace %q for user 2", consumer)
			clusterRole, clusterRoleBinding := createClusterRoleAndBindings("test-get-list", "user-2", "User", wildwest.GroupName, "cowboys", "", []string{"get", "list"})
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = kubeClusterClient.Cluster(consumer).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumer, []string{"user-2"}, nil, false)
			user2Client, err := wildwestclientset.NewForConfig(framework.StaticTokenUserConfig("user-2", rest.CopyConfig(cfg)))
			require.NoError(t, err)

			t.Logf("Make sure user 2 can list cowboys in consumer workspace %q", consumer)

			// This is needed to make sure the RBAC is updated in the informers
			require.Eventually(t, func() bool {
				_, err := user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(t.Context(), metav1.ListOptions{})
				return err == nil
			}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-2 to list cowboys")

			cowboy2 := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-2-cowboy",
					Namespace: "default",
				},
			}
			t.Logf("Make sure user 2 can not create cowboy resources in consumer workspace %q", consumer)
			_, err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").Create(t.Context(), cowboy2, metav1.CreateOptions{})
			require.Error(t, err)

			// Create user-2-cowboy for the admin to delete after the APIExport deletion.
			_, err = cowboyclient.Create(t.Context(), cowboy2, metav1.CreateOptions{})
			require.NoError(t, err)

			// Create another cowboy that will be deleted upon the deletion of the APIBinding.
			cowboy3 := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cowboy-3",
					Namespace: "default",
				},
			}
			_, err = cowboyclient.Create(t.Context(), cowboy3, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("User 2 can list cowboys in consumer workspace %q before deleting APIExport", consumer)
			user2Cowboys, err := user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Equal(t, 3, len(user2Cowboys.Items), "expected 3 cowboys in consumer")

			t.Logf("User 2 gets errors trying to delete an existing cowboy in consumer workspace %q", consumer)
			err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(t.Context(), cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.Error(t, err)

			t.Logf("Delete APIExport in provider workspace %q", rbacServiceProviderPath)
			err = kcpClusterClient.Cluster(rbacServiceProviderPath).ApisV1alpha2().APIExports().Delete(t.Context(), "today-cowboys", metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("Wait for APIExport deletion in provider workspace %q", rbacServiceProviderPath)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(rbacServiceProviderPath).ApisV1alpha2().APIExports().Get(t.Context(), "today-cowboys", metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, ""
				}
				if err != nil {
					return false, fmt.Sprintf("error getting APIExport: %v", err)
				}
				return false, "APIExport still exists"
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("User 2 can list cowboys in consumer workspace %q despite deleted APIExport", consumer)
			_, err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)

			// Due to RBAC - _not_ due to the deleted APIExport. This check is just to ensure the RBAC is not affected by the APIExport deletion.
			t.Logf("User 2 should get errors trying to delete an existing cowboy in consumer workspace %q", consumer)
			err = user2Client.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(t.Context(), cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.Error(t, err)

			t.Logf("Admin can list the cowboys in consumer workspace %q", consumer)
			cowboysAfterDelete, err := wildwestClusterClient.Cluster(consumer).WildwestV1alpha1().Cowboys("default").List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err, "error listing cowboys in consumer workspace %q", consumer)
			require.Equal(t, 3, len(cowboysAfterDelete.Items), "expected 3 cowboy in consumer")

			t.Logf("Admin can delete an existing cowboy in consumer workspace %q", consumer)
			err = wildwestClusterClient.Cluster(consumer).WildwestV1alpha1().Cowboys(cowboy2.ObjectMeta.Namespace).Delete(t.Context(), cowboy2.ObjectMeta.Name, metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("APIBinding deletion does not error")
			err = kcpClusterClient.Cluster(consumer).ApisV1alpha2().APIBindings().Delete(t.Context(), "cowboys", metav1.DeleteOptions{})
			require.NoError(t, err)

			t.Logf("Wait for APIBinding deletion in consumer workspace %q", consumer)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(consumer).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
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
			_, err = cowboyclient.Update(t.Context(), &cowboys.Items[0], metav1.UpdateOptions{})
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

	_, err = user3KcpClient.Cluster(consumer1Path).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
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

func setUpServiceProvider(t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, kubeClusterClient kcpkubernetesclientset.ClusterInterface, serviceProviderWorkspace, rbacServiceProvider logicalcluster.Path, cfg *rest.Config) {
	t.Helper()
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// permit user-3 to be able to bind the api export
	clusterRole, clusterRoleBinding := createClusterRoleAndBindings("user-3-binding", "user-3", "User", apisv1alpha2.SchemeGroupVersion.Group, "apiexports", "today-cowboys", []string{"bind"})
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(serviceProviderWorkspace).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)
}

func testCRUDOperations(t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface) {
	t.Helper()
	t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumer1Workspace)

	t.Logf("Make sure list shows nothing to start")
	cowboyClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	var cowboys *wildwestv1alpha1.CowboyList
	// Adding a poll here to wait for the user's to get access via RBAC informer updates.

	require.Eventually(t, func() bool {
		var err error
		cowboys, err = cowboyClient.List(t.Context(), metav1.ListOptions{})
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
		_, err := cowboyClient.Create(t.Context(), cowboy, metav1.CreateOptions{})
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

// TestMaximalPermissionPolicyServiceAccountClaimedTenancyResources tests that a ServiceAccount
// in a provider workspace can access claimed tenancy.kcp.io resources via the APIExport virtual
// workspace. This is a regression test for https://github.com/kcp-dev/kcp/issues/3840.
//
// The issue was that SA tokens are scoped to their originating workspace, but the maximal
// permission policy check runs in a different workspace (root for tenancy.kcp.io). Without
// stripping scope-related Extra fields from the user, the deep SAR would fail due to scope mismatch.
func TestMaximalPermissionPolicyServiceAccountClaimedTenancyResources(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	// Create workspaces under root for this test
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"))
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	t.Logf("Get the tenancy.kcp.io APIExport identity hash from root")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(t.Context(), "tenancy.kcp.io", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	tenancyAPIExport, err := kcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(t.Context(), "tenancy.kcp.io", metav1.GetOptions{})
	require.NoError(t, err)
	tenancyIdentityHash := tenancyAPIExport.Status.IdentityHash
	require.NotEmpty(t, tenancyIdentityHash, "tenancy.kcp.io identity hash should not be empty")
	t.Logf("Found tenancy.kcp.io identity hash: %s", tenancyIdentityHash)

	t.Logf("Install cowboys APIResourceSchema in provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport with permissionClaim on tenancy.kcp.io/workspaces in provider workspace %q", providerPath)
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildwest.dev",
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
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "tenancy.kcp.io",
						Resource: "workspaces",
					},
					IdentityHash: tenancyIdentityHash,
					Verbs:        []string{"get", "list", "watch"},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for APIExport to be ready with identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "wildwest.dev", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	t.Logf("Create namespace and ServiceAccount in provider workspace %q", providerPath)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-ns",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).CoreV1().Namespaces().Create(t.Context(), ns, metav1.CreateOptions{})
	require.NoError(t, err)

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "provider-sa",
			Namespace: "provider-ns",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("provider-ns").Create(t.Context(), sa, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Grant ServiceAccount apiexports/content access in provider workspace %q", providerPath)
	contentRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-sa-apiexport-content",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{"wildwest.dev"},
				Verbs:         []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), contentRole, metav1.CreateOptions{})
	require.NoError(t, err)

	contentRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "provider-sa-apiexport-content",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "provider-sa",
				Namespace: "provider-ns",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "provider-sa-apiexport-content",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), contentRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Grant bind permission on APIExport for consumer binding")
	bindRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "apiexport-bind",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports"},
				ResourceNames: []string{"wildwest.dev"},
				Verbs:         []string{"bind"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), bindRole, metav1.CreateOptions{})
	require.NoError(t, err)

	bindRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "apiexport-bind-authenticated",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "Group",
				Name:     "system:authenticated",
				APIGroup: rbacv1.SchemeGroupVersion.Group,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "apiexport-bind",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), bindRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create APIBinding in consumer workspace %q accepting the workspaces claim", consumerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wildwest",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "wildwest.dev",
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "tenancy.kcp.io",
								Resource: "workspaces",
							},
							IdentityHash: tenancyIdentityHash,
							Verbs:        []string{"get", "list", "watch"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating APIBinding: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for APIBinding to be ready")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Create a token for the ServiceAccount")
	tokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To(int64(3600)),
		},
	}
	tokenResp, err := kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("provider-ns").CreateToken(t.Context(), "provider-sa", tokenReq, metav1.CreateOptions{})
	require.NoError(t, err)
	saToken := tokenResp.Status.Token
	require.NotEmpty(t, saToken)
	t.Logf("Got SA token")

	t.Logf("Get the APIExport virtual workspace URL")
	var apiExportVWURL string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), "wildwest.dev", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting APIExportEndpointSlice: %v", err)
		}
		urls := framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice)
		var found bool
		apiExportVWURL, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClusterClient, consumerWorkspace, urls)
		if err != nil {
			return false, fmt.Sprintf("error getting virtual workspace URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URL to be available: %v", urls)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Got APIExport virtual workspace URL: %s", apiExportVWURL)

	t.Logf("Create a client using the ServiceAccount token to access the virtual workspace")
	saConfig := rest.CopyConfig(cfg)
	saConfig.Host = apiExportVWURL
	saConfig.BearerToken = saToken
	// Clear cert-based auth since we're using token
	saConfig.CertData = nil
	saConfig.KeyData = nil
	saConfig.CertFile = ""
	saConfig.KeyFile = ""

	saDynamicClient, err := kcpdynamic.NewForConfig(saConfig)
	require.NoError(t, err)

	t.Logf("Verify that the ServiceAccount can list workspaces via the virtual workspace (claimed tenancy.kcp.io resource)")
	// This is the key test - before the fix, this would return 403 because the SA token
	// is scoped to the provider workspace, but the maximal permission policy check runs
	// in the root workspace where tenancy.kcp.io lives.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := saDynamicClient.Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces")).List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing workspaces: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "ServiceAccount should be able to list claimed workspaces resource")

	t.Logf("Test passed - ServiceAccount can access claimed tenancy.kcp.io resources via virtual workspace")

	// Now test the negative case: create another ServiceAccount WITHOUT apiexports/content permission
	// and verify it cannot access the claimed resources
	t.Logf("Create another ServiceAccount WITHOUT apiexports/content access")
	unauthorizedSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unauthorized-sa",
			Namespace: "provider-ns",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("provider-ns").Create(t.Context(), unauthorizedSA, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create a token for the unauthorized ServiceAccount")
	unauthorizedTokenReq := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To(int64(3600)),
		},
	}
	unauthorizedTokenResp, err := kubeClusterClient.Cluster(providerPath).CoreV1().ServiceAccounts("provider-ns").CreateToken(t.Context(), "unauthorized-sa", unauthorizedTokenReq, metav1.CreateOptions{})
	require.NoError(t, err)
	unauthorizedToken := unauthorizedTokenResp.Status.Token
	require.NotEmpty(t, unauthorizedToken)

	t.Logf("Create a client using the unauthorized ServiceAccount token")
	unauthorizedConfig := rest.CopyConfig(cfg)
	unauthorizedConfig.Host = apiExportVWURL
	unauthorizedConfig.BearerToken = unauthorizedToken
	unauthorizedConfig.CertData = nil
	unauthorizedConfig.KeyData = nil
	unauthorizedConfig.CertFile = ""
	unauthorizedConfig.KeyFile = ""

	unauthorizedDynamicClient, err := kcpdynamic.NewForConfig(unauthorizedConfig)
	require.NoError(t, err)

	t.Logf("Verify that the unauthorized ServiceAccount CANNOT list workspaces via the virtual workspace")
	// Without apiexports/content permission, the SA should get a 403 Forbidden
	_, err = unauthorizedDynamicClient.Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces")).List(t.Context(), metav1.ListOptions{})
	require.Error(t, err, "unauthorized ServiceAccount should not be able to list workspaces")
	require.True(t, apierrors.IsForbidden(err), "expected Forbidden error, got: %v", err)

	t.Logf("Negative test passed - unauthorized ServiceAccount correctly denied access to claimed resources")

	// IMPORTANT: This behaviour is intentional. We might want to change this in the future, but now - no.
	// Test with a real user (not a ServiceAccount) to verify they CANNOT access claimed resources
	// because real users also have scopes attached when accessing via virtual workspace, and we
	// only strip scopes for ServiceAccounts (not regular users).
	// This is the expected behavior - regular users should not be able to bypass scope restrictions.
	t.Logf("Test with a real user (not ServiceAccount) accessing claimed resources")

	t.Logf("Grant user-1 apiexports/content access in provider workspace %q", providerPath)
	userContentRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-1-apiexport-content",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"apis.kcp.io"},
				Resources:     []string{"apiexports/content"},
				ResourceNames: []string{"wildwest.dev"},
				Verbs:         []string{"*"},
			},
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), userContentRole, metav1.CreateOptions{})
	require.NoError(t, err)

	userContentRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-1-apiexport-content",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				Name:     "user-1",
				APIGroup: rbacv1.SchemeGroupVersion.Group,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "user-1-apiexport-content",
		},
	}
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), userContentRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create a client for user-1 to access the virtual workspace")
	user1Config := framework.StaticTokenUserConfig("user-1", rest.CopyConfig(cfg))
	user1Config.Host = apiExportVWURL

	user1DynamicClient, err := kcpdynamic.NewForConfig(user1Config)
	require.NoError(t, err)

	t.Logf("Verify that user-1 CANNOT list workspaces via the virtual workspace (claimed tenancy.kcp.io resource)")
	// Real users also have scope restrictions when accessing via the virtual workspace.
	// Unlike ServiceAccounts, we do NOT strip scopes from regular users, so they cannot
	// access claimed resources from APIExports in workspaces they don't have scope on.
	// This is the expected and secure behavior.
	_, err = user1DynamicClient.Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces")).List(t.Context(), metav1.ListOptions{})
	require.Error(t, err, "user-1 should not be able to list workspaces due to scope restrictions")
	require.True(t, apierrors.IsForbidden(err), "expected Forbidden error for user-1 due to scope restrictions, got: %v", err)

	t.Logf("Real user test passed - user-1 correctly denied access due to scope restrictions (scopes not stripped for regular users)")
}
