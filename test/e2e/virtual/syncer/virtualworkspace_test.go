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

package workspace

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func deploymentsAPIResourceList(workspaceName logicalcluster.Name) *metav1.APIResourceList {
	return &metav1.APIResourceList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIResourceList",
			APIVersion: "v1",
		},
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{
			{
				Kind:               "Deployment",
				Name:               "deployments",
				SingularName:       "deployment",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "apps", "v1", "Deployment"),
				Categories:         []string{"all"},
				ShortNames:         []string{"deploy"},
			},
			{
				Kind:               "Deployment",
				Name:               "deployments/status",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "patch", "update"},
				StorageVersionHash: "",
			},
		},
	}
}

func requiredCoreAPIResourceList(workspaceName logicalcluster.Name) *metav1.APIResourceList {
	return &metav1.APIResourceList{
		TypeMeta: metav1.TypeMeta{
			Kind: "APIResourceList",
		},
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{
				Kind:               "ConfigMap",
				Name:               "configmaps",
				SingularName:       "configmap",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "ConfigMap"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces",
				SingularName:       "namespace",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "Namespace"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces/status",
				SingularName:       "",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "patch", "update"},
				StorageVersionHash: "",
			},
			{
				Kind:               "Secret",
				Name:               "secrets",
				SingularName:       "secret",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "Secret"),
			},
			{
				Kind:               "ServiceAccount",
				Name:               "serviceaccounts",
				SingularName:       "serviceaccount",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "ServiceAccount"),
			},
		},
	}
}

func addToAPIResourceList(list *metav1.APIResourceList, resources ...metav1.APIResource) *metav1.APIResourceList {
	list.APIResources = append(list.APIResources, resources...)

	return list
}

func TestSyncerVirtualWorkspace(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)
	wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	var testCases = []struct {
		name string
		work func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string)
	}{
		{
			name: "isolated API domains per syncer",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				kubelikeVWDiscoverClusterClient, err := clientgodiscovery.NewDiscoveryClientForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				t.Logf("Check discovery in kubelike virtual workspace")
				_, kubelikeAPIResourceLists, err := kubelikeVWDiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
				require.NoError(t, err)
				require.Empty(t, cmp.Diff([]*metav1.APIResourceList{
					deploymentsAPIResourceList(kubelikeClusterName),
					{
						TypeMeta: metav1.TypeMeta{
							Kind:       "APIResourceList",
							APIVersion: "v1",
						},
						GroupVersion: "networking.k8s.io/v1",
						APIResources: []metav1.APIResource{
							{
								Kind:               "Ingress",
								Name:               "ingresses",
								SingularName:       "ingress",
								Namespaced:         true,
								Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
								ShortNames:         []string{"ing"},
								StorageVersionHash: discovery.StorageVersionHash(kubelikeClusterName, "networking.k8s.io", "v1", "Ingress"),
							},
							{
								Kind:               "Ingress",
								Name:               "ingresses/status",
								Namespaced:         true,
								Verbs:              metav1.Verbs{"get", "patch", "update"},
								StorageVersionHash: "",
							},
						},
					},
					addToAPIResourceList(
						requiredCoreAPIResourceList(kubelikeClusterName),
						metav1.APIResource{
							Kind:               "Service",
							Name:               "services",
							SingularName:       "service",
							Namespaced:         true,
							Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
							StorageVersionHash: discovery.StorageVersionHash(kubelikeClusterName, "", "v1", "Service"),
							Categories:         []string{"all"},
							ShortNames:         []string{"svc"},
						},
						metav1.APIResource{
							Kind:               "Service",
							Name:               "services/status",
							SingularName:       "",
							Namespaced:         true,
							Verbs:              metav1.Verbs{"get", "patch", "update"},
							StorageVersionHash: "",
						}),
				}, sortAPIResourceList(kubelikeAPIResourceLists)))

				t.Logf("Check discovery in wildwest virtual workspace")
				wildwestVWDiscoverClusterClient, err := clientgodiscovery.NewDiscoveryClientForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)
				_, wildwestAPIResourceLists, err := wildwestVWDiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
				require.NoError(t, err)
				require.Empty(t, cmp.Diff([]*metav1.APIResourceList{
					deploymentsAPIResourceList(wildwestClusterName),
					requiredCoreAPIResourceList(wildwestClusterName),
					{
						TypeMeta: metav1.TypeMeta{
							Kind:       "APIResourceList",
							APIVersion: "v1",
						},
						GroupVersion: "wildwest.dev/v1alpha1",
						APIResources: []metav1.APIResource{
							{
								Kind:               "Cowboy",
								Name:               "cowboys",
								SingularName:       "cowboy",
								Namespaced:         true,
								Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
								StorageVersionHash: discovery.StorageVersionHash(wildwestClusterName, "wildwest.dev", "v1alpha1", "Cowboy"),
							},
							{
								Kind:               "Cowboy",
								Name:               "cowboys/status",
								Namespaced:         true,
								Verbs:              metav1.Verbs{"get", "patch", "update"},
								StorageVersionHash: "",
							},
						},
					},
				}, sortAPIResourceList(wildwestAPIResourceLists)))
			},
		},
		{
			name: "access is authorized",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				t.Logf("Create two service accounts")
				_, err := kubeClusterClient.CoreV1().ServiceAccounts("default").Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-1",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.CoreV1().ServiceAccounts("default").Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-2",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				var token1, token2 string
				require.Eventually(t, func() bool {
					secrets, err := kubeClusterClient.CoreV1().Secrets("default").List(logicalcluster.WithCluster(ctx, wildwestClusterName), metav1.ListOptions{})
					require.NoError(t, err, "failed to list secrets")
					for _, secret := range secrets.Items {
						if secret.Annotations[corev1.ServiceAccountNameKey] == "service-account-1" {
							token1 = string(secret.Data[corev1.ServiceAccountTokenKey])
						}
						if secret.Annotations[corev1.ServiceAccountNameKey] == "service-account-2" {
							token2 = string(secret.Data[corev1.ServiceAccountTokenKey])
						}
					}
					return token1 != "" && token2 != ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "token secret for default service account not created")

				configUser1 := framework.ConfigWithToken(token1, wildwestSyncerVWConfig)

				configUser2 := framework.ConfigWithToken(token2, wildwestSyncerVWConfig)

				vwClusterClientUser1, err := wildwestclientset.NewForConfig(configUser1)
				require.NoError(t, err)
				vwClusterClientUser2, err := wildwestclientset.NewForConfig(configUser2)
				require.NoError(t, err)

				t.Logf("Check discovery in wildwest virtual workspace with unprivileged service-account-1, expecting forbidden")
				_, err = vwClusterClientUser1.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
				require.Error(t, err)
				require.True(t, errors.IsForbidden(err))

				t.Logf("Giving service-account-2 permissions to access wildwest virtual workspace")
				_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, wildwestClusterName),
					&rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{
							Name: "service-account-2-sync-access",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "ServiceAccount",
								Name:      "service-account-2",
								Namespace: "default",
							},
						},
						RoleRef: rbacv1.RoleRef{
							APIGroup: "rbac.authorization.k8s.io",
							Kind:     "ClusterRole",
							Name:     wildwestSyncTargetName + "-syncer",
						},
					}, metav1.CreateOptions{},
				)
				require.NoError(t, err)
				_, err = kubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, wildwestClusterName),
					&rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{
							Name: wildwestSyncTargetName + "-syncer",
						},
						Rules: []rbacv1.PolicyRule{
							{
								Verbs:         []string{"sync"},
								APIGroups:     []string{"workload.kcp.dev"},
								Resources:     []string{"synctargets"},
								ResourceNames: []string{wildwestSyncTargetName},
							},
						},
					}, metav1.CreateOptions{},
				)
				require.NoError(t, err)

				t.Logf("Check discovery in wildwest virtual workspace with unprivileged service-account-2, expecting success")
				framework.Eventually(t, func() (bool, string) {
					_, err = vwClusterClientUser2.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil, fmt.Sprintf("waiting for service-account-2 to be able to list cowboys: %v", err)
				}, wait.ForeverTestTimeout, time.Millisecond*200)

				t.Logf("Double check that service-account-1 still cannot access wildwest virtual workspace")
				_, err = vwClusterClientUser1.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
				require.Error(t, err)
				require.True(t, errors.IsForbidden(err))
			},
		},
		{
			name: "access kcp resources through syncer virtual workspace",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				t.Log("Create cowboy luckyluke")
				_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, wildwestClusterName), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				t.Log("Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got: %s", toYAML(t, cowboys))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				t.Log("Verify there is luckyluke via virtual workspace workspace request")
				kcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)
				require.Equal(t, kcpCowboy.Spec, virtualWorkspaceCowboy.Spec)
				require.Equal(t, kcpCowboy.Status, virtualWorkspaceCowboy.Status)

				t.Log("Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Log("Verify that luckyluke has both spec and status changed")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, modifiedkcpCowboy)

				t.Log("Verify resource version, managed fields and generation")
				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch averell"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, modifiedkcpCowboy.Spec))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, modifiedkcpCowboy.Status))
			},
		},
		{
			name: "access kcp resources through syncer virtual workspace, from a other workspace to the wildwest resources through an APIBinding",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kcpClusterClient, err := kcpclient.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				otherWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

				t.Logf("Create a binding in the other workspace")
				binding := &apisv1alpha1.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kubernetes",
					},
					Spec: apisv1alpha1.APIBindingSpec{
						Reference: apisv1alpha1.ExportReference{
							Workspace: &apisv1alpha1.WorkspaceExportReference{
								Path:       wildwestClusterName.String(),
								ExportName: "kubernetes",
							},
						},
					},
				}
				_, err = kcpClusterClient.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, otherWorkspace), binding, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Logf("Wait for binding to be ready")
				require.Eventually(t, func() bool {
					binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, otherWorkspace), binding.Name, metav1.GetOptions{})
					if err != nil {
						klog.Errorf("Failed to list Locations: %v", err)
						return false
					}
					return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted)
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Logf("Wait for binding to have cowboy resource")
				require.Eventually(t, func() bool {
					binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, otherWorkspace), binding.Name, metav1.GetOptions{})
					if err != nil {
						klog.Errorf("Failed to list Locations: %v", err)
						return false
					}
					for _, r := range binding.Status.BoundResources {
						if r.Resource == "cowboys" {
							return true
						}
					}
					return false
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				t.Logf("Wait for being able to list cowboys in the other workspace (kubelike) through the virtual workspace")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						klog.Errorf("Failed to list Services: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Create cowboy luckyluke")
				_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, otherWorkspace), &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				t.Log("Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got: %s", toYAML(t, cowboys))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				t.Log("Verify there is luckyluke via virtual workspace")
				kcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)
				require.Empty(t, cmp.Diff(kcpCowboy.Spec, virtualWorkspaceCowboy.Spec))
				require.Empty(t, cmp.Diff(kcpCowboy.Status, virtualWorkspaceCowboy.Status))

				t.Log("Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Log("Verify that luckyluke has both spec and status changed")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, modifiedkcpCowboy)

				t.Log("Verify resource version, managed fields and generation")
				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch averell"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, modifiedkcpCowboy.Spec))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, modifiedkcpCowboy.Status))
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			kubelikeWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

			t.Logf("Deploying syncer into workspace %s", kubelikeWorkspace)
			kubelikeSyncer := framework.NewSyncerFixture(t, server, kubelikeWorkspace,
				framework.WithSyncTarget(kubelikeWorkspace, "kubelike"),
				framework.WithExtraResources("ingresses.networking.k8s.io", "services"),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					if !isFakePCluster {
						// Only need to install services and ingresses in a logical cluster
						return
					}
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err, "failed to create apiextensions client")
					t.Logf("Installing test CRDs into sink cluster...")
					kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
						metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
						metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
					)
					require.NoError(t, err)
				}),
			).Start(t)

			t.Log("Waiting for ingresses crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.NetworkingV1().Ingresses("").List(logicalcluster.WithCluster(ctx, kubelikeWorkspace), metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for ingresses crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Waiting for services crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, kubelikeWorkspace), metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for services crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Setting up an unrelated workspace with cowboys...")

			unrelatedWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

			sourceCrdClient, err := apiextensionsclientset.NewForConfig(server.BaseConfig(t))
			require.NoError(t, err)

			fixturewildwest.Create(t, unrelatedWorkspace, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})

			t.Log("Waiting for cowboys crd to be imported and available in the kubelike workspace...")
			require.Eventually(t, func() bool {
				_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, unrelatedWorkspace), metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for cowboys crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Create cowboy unluckyluke")
			_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, unrelatedWorkspace), &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "unluckyluke",
				},
				Spec: wildwestv1alpha1.CowboySpec{
					Intent: "will never catch joe",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			wildwestWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
			wildwestSyncTargetName := fmt.Sprintf("wildwest-%d", +rand.Intn(1000000))

			t.Logf("Deploying syncer into workspace %s", wildwestWorkspace)
			wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestWorkspace,
				framework.WithExtraResources("cowboys.wildwest.dev"),
				framework.WithSyncTarget(wildwestWorkspace, wildwestSyncTargetName),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					// Always install the crd regardless of whether the target is
					// logical or not since cowboys is not a native type.
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err)
					t.Log("Installing test CRDs into sink cluster...")
					fixturewildwest.Create(t, logicalcluster.Name{}, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
				}),
			).Start(t)

			t.Log("Waiting for cowboys crd to be imported and available in the wildwest source workspace...")
			require.Eventually(t, func() bool {
				_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, wildwestWorkspace), metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for cowboys crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			// create virtual workspace rest configs
			rawConfig, err := server.RawConfig()
			require.NoError(t, err)
			virtualWorkspaceRawConfig := rawConfig.DeepCopy()
			virtualWorkspaceRawConfig.Clusters["kubelike"] = rawConfig.Clusters["base"].DeepCopy()
			virtualWorkspaceRawConfig.Clusters["kubelike"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + kubelikeWorkspace.String() + "/kubelike/" + kubelikeSyncer.SyncerConfig.SyncTargetUID
			virtualWorkspaceRawConfig.Contexts["kubelike"] = rawConfig.Contexts["base"].DeepCopy()
			virtualWorkspaceRawConfig.Contexts["kubelike"].Cluster = "kubelike"
			virtualWorkspaceRawConfig.Clusters["wildwest"] = rawConfig.Clusters["base"].DeepCopy()
			virtualWorkspaceRawConfig.Clusters["wildwest"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + wildwestWorkspace.String() + "/" + wildwestSyncTargetName + "/" + wildwestSyncer.SyncerConfig.SyncTargetUID
			virtualWorkspaceRawConfig.Contexts["wildwest"] = rawConfig.Contexts["base"].DeepCopy()
			virtualWorkspaceRawConfig.Contexts["wildwest"].Cluster = "wildwest"
			kubelikeVWConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "kubelike", nil, nil).ClientConfig()
			kubelikeVWConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(kubelikeVWConfig), t.Name()))
			require.NoError(t, err)
			wildwestVWConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "wildwest", nil, nil).ClientConfig()
			wildwestVWConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(wildwestVWConfig), t.Name()))
			require.NoError(t, err)

			t.Log("Starting test...")
			testCase.work(t, kubelikeVWConfig, wildwestVWConfig, kubelikeWorkspace, wildwestWorkspace, wildwestSyncTargetName)
		})
	}
}

func toYAML(t *testing.T, obj interface{}) string {
	bs, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(bs)
}

type ByGroupVersion []*metav1.APIResourceList

func (a ByGroupVersion) Len() int           { return len(a) }
func (a ByGroupVersion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByGroupVersion) Less(i, j int) bool { return a[i].GroupVersion < a[j].GroupVersion }

type ByName []metav1.APIResource

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].Name < a[j].Name }

func sortAPIResourceList(list []*metav1.APIResourceList) []*metav1.APIResourceList {
	sort.Sort(ByGroupVersion(list))
	for _, resource := range list {
		sort.Sort(ByName(resource.APIResources))
	}
	return list
}
