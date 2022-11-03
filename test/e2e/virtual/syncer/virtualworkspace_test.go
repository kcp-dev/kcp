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

package syncer

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpapiextensionsclientset "github.com/kcp-dev/apiextensions-apiserver/pkg/client/clientset/versioned"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config/rootcompute"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
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

func withRootComputeAPIResourceList(workspaceName logicalcluster.Name) []*metav1.APIResourceList {
	coreResourceList := requiredCoreAPIResourceList(workspaceName)
	coreResourceList.APIResources = append(coreResourceList.APIResources,
		metav1.APIResource{
			Kind:               "Service",
			Name:               "services",
			SingularName:       "service",
			Namespaced:         true,
			Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
			ShortNames:         []string{"svc"},
			Categories:         []string{"all"},
			StorageVersionHash: discovery.StorageVersionHash(rootcompute.RootComputeWorkspace, "", "v1", "Service"),
		},
		metav1.APIResource{
			Kind:               "Service",
			Name:               "services/status",
			SingularName:       "",
			Namespaced:         true,
			Verbs:              metav1.Verbs{"get", "patch", "update"},
			StorageVersionHash: "",
		},
	)

	return []*metav1.APIResourceList{
		deploymentsAPIResourceList(rootcompute.RootComputeWorkspace),
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
					StorageVersionHash: discovery.StorageVersionHash(rootcompute.RootComputeWorkspace, "networking.k8s.io", "v1", "Ingress"),
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
		coreResourceList,
	}
}

func TestSyncerVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
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
				require.Eventually(t, func() bool {
					_, kubelikeAPIResourceLists, err := kubelikeVWDiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
					if err != nil {
						return false
					}
					return len(cmp.Diff(
						withRootComputeAPIResourceList(kubelikeClusterName),
						sortAPIResourceList(kubelikeAPIResourceLists))) == 0
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Logf("Check discovery in wildwest virtual workspace")
				wildwestVWDiscoverClusterClient, err := clientgodiscovery.NewDiscoveryClientForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					_, wildwestAPIResourceLists, err := wildwestVWDiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
					if err != nil {
						return false
					}
					return len(cmp.Diff([]*metav1.APIResourceList{
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
					}, sortAPIResourceList(wildwestAPIResourceLists))) == 0
				}, wait.ForeverTestTimeout, time.Millisecond*100)
			},
		},
		{
			name: "access is authorized",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				t.Logf("Create two service accounts")
				_, err := kubeClusterClient.Cluster(wildwestClusterName).CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-1",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(wildwestClusterName).CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-2",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				var token1, token2 string
				require.Eventually(t, func() bool {
					secrets, err := kubeClusterClient.Cluster(wildwestClusterName).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
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
				_, err = kubeClusterClient.Cluster(wildwestClusterName).RbacV1().ClusterRoleBindings().Create(ctx,
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
				_, err = kubeClusterClient.Cluster(wildwestClusterName).RbacV1().ClusterRoles().Create(ctx,
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

				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(wildwestClusterName, wildwestSyncTargetName)

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
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
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

				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncTargetsToSync := map[string]string{}
					for name, value := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							syncTargetsToSync[strings.TrimPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix)] = value
						}
					}

					syncTargetsWithFinalizer := sets.NewString()
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncTargetsWithFinalizer.Insert(strings.TrimPrefix(name, shared.SyncerFinalizerNamePrefix))
						}
					}

					return len(syncTargetsToSync) == 1 &&
						syncTargetsToSync[syncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(syncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond)

				t.Log("Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Log("Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				t.Log("Verify that luckyluke has also status changed on the upstream view, since the status field is promoted by default")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)
				require.Equal(t, virtualWorkspaceModifiedkcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, modifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, modifiedkcpCowboy.Spec))
			},
		},
		{
			name: "access kcp resources through syncer virtual workspace, from a other workspace to the wildwest resources through an APIBinding",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				otherWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

				t.Logf("Bind wildwest workspace")
				framework.NewBindCompute(t, otherWorkspace, server,
					framework.WithAPIExportsWorkloadBindOption(wildwestClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestClusterName),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				t.Logf("Wait for being able to list cowboys in the other workspace (kubelike) via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						t.Logf("Failed to list Services: %v", err)
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
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
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

				t.Log("Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				t.Log("Verify that luckyluke has also status changed on the upstream view, since the status field is promoted by default")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)
				require.Equal(t, virtualWorkspaceModifiedkcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, modifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, modifiedkcpCowboy.Spec))
			},
		},
		{
			name: "Never promote overridden syncer view status to upstream when scheduled on 2 synctargets",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kcpClusterClient, err := kcpclient.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), wildwestSyncTargetName, types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/name","value":"`+wildwestSyncTargetName+`"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Logf("Deploying second syncer into workspace %s", wildwestClusterName)
				wildwestSecondSyncTargetName := wildwestSyncTargetName + "second"
				wildwestSecondSyncer := framework.NewSyncerFixture(t, server, wildwestClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev", "services"),
					framework.WithSyncTarget(wildwestClusterName, wildwestSecondSyncTargetName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						t.Log("Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
						if isFakePCluster {
							// Only need to install services in a logical cluster
							kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
								metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
							)
						}
					}),
				).Start(t)
				rawConfig, err := server.RawConfig()
				require.NoError(t, err)
				wildwestSecondRawConfig := rawConfig.DeepCopy()
				wildwestSecondRawConfig.Clusters["base"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + wildwestClusterName.String() + "/" + wildwestSecondSyncTargetName + "/" + wildwestSecondSyncer.SyncerConfig.SyncTargetUID
				wildwestSecondSyncerVWConfig, err := clientcmd.NewNonInteractiveClientConfig(*wildwestSecondRawConfig, "base", nil, nil).ClientConfig()
				require.NoError(t, err)
				wildwestSecondSyncerVWConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(wildwestSecondSyncerVWConfig), t.Name()))

				_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), wildwestSecondSyncTargetName, types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/name","value":"`+wildwestSecondSyncTargetName+`"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				otherWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

				t.Logf("Create 2 locations, one for each SyncTarget")
				_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "firstlocation",
						Labels: map[string]string{
							"name": wildwestSyncTargetName,
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name": wildwestSyncTargetName,
							},
						},
						Resource: schedulingv1alpha1.GroupVersionResource{
							Group:    "workload.kcp.dev",
							Version:  "v1alpha1",
							Resource: "synctargets",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "secondlocation",
						Labels: map[string]string{
							"name": wildwestSecondSyncTargetName,
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name": wildwestSecondSyncTargetName,
							},
						},
						Resource: schedulingv1alpha1.GroupVersionResource{
							Group:    "workload.kcp.dev",
							Version:  "v1alpha1",
							Resource: "synctargets",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Logf("Create 2 placements, one for each SyncTarget")
				framework.NewBindCompute(t, otherWorkspace, server,
					framework.WithPlacementNameBindOption("firstplacement"),
					framework.WithAPIExportsWorkloadBindOption(wildwestClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestClusterName),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"name": wildwestSyncTargetName,
						},
					}),
				).Bind(t)

				framework.NewBindCompute(t, otherWorkspace, server,
					framework.WithPlacementNameBindOption("secondplacement"),
					framework.WithAPIExportsWorkloadBindOption(wildwestClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestClusterName),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"name": wildwestSecondSyncTargetName,
						},
					}),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				t.Logf("Wait for being able to list cowboys in the other workspace (kubelike) through the virtual workspace")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						t.Logf("ERROR: Failed to list Services: %v", err)
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

				vwSyncerClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)

				vwSecondSyncerClusterClient, err := wildwestclientset.NewForConfig(wildwestSecondSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				t.Log("Wait until the first virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSyncerClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Wait until the second virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSyncerClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Wait for resource controller to schedule cowboy on the 2 synctargets")
				var kcpCowboy *wildwestv1alpha1.Cowboy
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					resourceStateLabelCount := 0
					for name := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							resourceStateLabelCount++
						}
					}

					return resourceStateLabelCount == 2
				}, wait.ForeverTestTimeout, time.Millisecond)

				t.Log("Wait for the 2 syncers to own lukyluke")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncerFinalizerCount := 0
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncerFinalizerCount++
						}
					}

					return syncerFinalizerCount == 2
				}, wait.ForeverTestTimeout, time.Millisecond)

				t.Log("Patch luckyluke via first virtual workspace to report in status that joe is in prison one")
				_, err = vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison one\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via second virtual workspace to report in status that joe is in prison two")
				_, err = vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison two\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Verify that luckyluke has status changed on the syncer view of first syncer")
				firstVirtualWorkspaceModifiedkcpCowboy, err := vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison one", firstVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				t.Log("Verify that luckyluke has status changed on the syncer view of second syncer")
				secondVirtualWorkspaceModifiedkcpCowboy, err := vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison two", secondVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				t.Log("Verify that luckyluke has status unchanged on the upstream view, since the status field is never promoted when a resource is scheduled to 2 synctargets")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.Status, modifiedkcpCowboy.Status)
			},
		},
		{
			name: "Correctly manage status, with promote and unpromote, when moving a cowboy from one synctarget to the other",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kcpClusterClient, err := kcpclient.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), wildwestSyncTargetName, types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/name","value":"`+wildwestSyncTargetName+`"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Logf("Deploying second syncer into workspace %s", wildwestClusterName)
				wildwestSecondSyncTargetName := wildwestSyncTargetName + "second"
				wildwestSecondSyncer := framework.NewSyncerFixture(t, server, wildwestClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					framework.WithSyncTarget(wildwestClusterName, wildwestSecondSyncTargetName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						t.Log("Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
						if isFakePCluster {
							// Only need to install services in a logical cluster
							sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
							require.NoError(t, err)
							kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
								metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
							)
						}
					}),
				).Start(t)
				rawConfig, err := server.RawConfig()
				require.NoError(t, err)
				wildwestSecondRawConfig := rawConfig.DeepCopy()
				wildwestSecondRawConfig.Clusters["base"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + wildwestClusterName.String() + "/" + wildwestSecondSyncTargetName + "/" + wildwestSecondSyncer.SyncerConfig.SyncTargetUID
				wildwestSecondSyncerVWConfig, err := clientcmd.NewNonInteractiveClientConfig(*wildwestSecondRawConfig, "base", nil, nil).ClientConfig()
				require.NoError(t, err)
				wildwestSecondSyncerVWConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(wildwestSecondSyncerVWConfig), t.Name()))

				_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), wildwestSecondSyncTargetName, types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/name","value":"`+wildwestSecondSyncTargetName+`"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Logf("Delete default location")
				err = kcpClusterClient.SchedulingV1alpha1().Locations().Delete(logicalcluster.WithCluster(ctx, wildwestClusterName), "default", metav1.DeleteOptions{})
				require.NoError(t, err)

				t.Logf("Create 2 locations, one for each SyncTarget")
				_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "firstlocation",
						Labels: map[string]string{
							"name": wildwestSyncTargetName,
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name": wildwestSyncTargetName,
							},
						},
						Resource: schedulingv1alpha1.GroupVersionResource{
							Group:    "workload.kcp.dev",
							Version:  "v1alpha1",
							Resource: "synctargets",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "secondlocation",
						Labels: map[string]string{
							"name": wildwestSecondSyncTargetName,
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name": wildwestSecondSyncTargetName,
							},
						},
						Resource: schedulingv1alpha1.GroupVersionResource{
							Group:    "workload.kcp.dev",
							Version:  "v1alpha1",
							Resource: "synctargets",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				otherWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
				t.Logf("Using User workspace: %s", otherWorkspace.String())

				logWithTimestamp := func(format string, args ...interface{}) {
					t.Logf("[%s] %s", time.Now().Format("15:04:05.000000"), fmt.Sprintf(format, args...))
				}

				logWithTimestamp("Create a first placement, for the first SyncTarget")
				framework.NewBindCompute(t, otherWorkspace, server,
					framework.WithPlacementNameBindOption("firstplacement"),
					framework.WithAPIExportsWorkloadBindOption(wildwestClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestClusterName),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"name": wildwestSyncTargetName,
						},
					}),
				).Bind(t)

				logWithTimestamp("Wait for being able to list cowboys in the other workspace (kubelike) through the virtual workspace")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp("ERROR: Failed to list Services: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				vwSyncerClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncerVWConfig)
				require.NoError(t, err)
				vwSecondSyncerClusterClient, err := wildwestclientset.NewForConfig(wildwestSecondSyncerVWConfig)
				require.NoError(t, err)

				logWithTimestamp("Wait until the first virtual workspace has the resource type")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSyncerClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp("Wait until the second virtual workspace has the resource type")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp("Create cowboy luckyluke")
				_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, otherWorkspace), &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp("Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("").List(logicalcluster.WithCluster(ctx, otherWorkspace), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				firstSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(wildwestClusterName, wildwestSyncTargetName)
				secondSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(wildwestClusterName, wildwestSecondSyncTargetName)

				logWithTimestamp("Wait for resource controller to schedule cowboy on the first synctarget, and for the syncer to own it")
				var kcpCowboy *wildwestv1alpha1.Cowboy
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncTargetsToSync := map[string]string{}
					for name, value := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							syncTargetsToSync[strings.TrimPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix)] = value
						}
					}

					syncTargetsWithFinalizer := sets.NewString()
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncTargetsWithFinalizer.Insert(strings.TrimPrefix(name, shared.SyncerFinalizerNamePrefix))
						}
					}

					return len(syncTargetsToSync) == 1 &&
						syncTargetsToSync[firstSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(firstSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond)

				logWithTimestamp("Patch luckyluke via first virtual workspace to report in status that joe is in prison one")
				_, err = vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison one\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp("Verify that luckyluke has status changed on the syncer view of first syncer")
				firstVirtualWorkspaceModifiedkcpCowboy, err := vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison one", firstVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp("Verify that luckyluke has also status changed on the upstream view, since the status field is promoted when scheduled on only one synctarget")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison one", modifiedkcpCowboy.Status.Result)

				logWithTimestamp("Create the placement for the second SyncTarget")
				framework.NewBindCompute(t, otherWorkspace, server,
					framework.WithPlacementNameBindOption("secondplacement"),
					framework.WithAPIExportsWorkloadBindOption(wildwestClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestClusterName),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"name": wildwestSecondSyncTargetName,
						},
					}),
				).Bind(t)

				logWithTimestamp("Wait for resource controller to schedule cowboy on the 2 synctargets, and for both syncers to own it")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncTargetsToSync := map[string]string{}
					for name, value := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							syncTargetsToSync[strings.TrimPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix)] = value
						}
					}

					syncTargetsWithFinalizer := sets.NewString()
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncTargetsWithFinalizer.Insert(strings.TrimPrefix(name, shared.SyncerFinalizerNamePrefix))
						}
					}

					return len(syncTargetsToSync) == 2 &&
						syncTargetsToSync[firstSyncTargetKey] == "Sync" &&
						syncTargetsToSync[secondSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 2 &&
						syncTargetsWithFinalizer.Has(firstSyncTargetKey) &&
						syncTargetsWithFinalizer.Has(secondSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond)

				logWithTimestamp("Patch luckyluke via second virtual workspace to report in status that joe is in prison two")
				_, err = vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison two\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp("Verify that luckyluke has status unchanged on the syncer view of the first syncer")
				firstVirtualWorkspaceModifiedkcpCowboy, err = vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison one", firstVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp("Verify that luckyluke has status changed on the syncer view of second syncer")
				secondVirtualWorkspaceModifiedkcpCowboy, err := vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison two", secondVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp("Verify that luckyluke has status unchanged on the upstream view, since no syncer view status has been promoted since the last promotion, because scheduled on 2 synctargets")
				modifiedkcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison one", modifiedkcpCowboy.Status.Result)

				logWithTimestamp("Remove the placement for the first SyncTarget")
				err = kcpClusterClient.SchedulingV1alpha1().Placements().Delete(logicalcluster.WithCluster(ctx, otherWorkspace), "firstplacement", metav1.DeleteOptions{})
				require.NoError(t, err)

				logWithTimestamp("Wait for resource controller to schedule cowboy on the second synctarget only, and for the second syncer only to own it")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncTargetsToSync := map[string]string{}
					for name, value := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							syncTargetsToSync[strings.TrimPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix)] = value
						}
					}

					syncTargetsWithFinalizer := sets.NewString()
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncTargetsWithFinalizer.Insert(strings.TrimPrefix(name, shared.SyncerFinalizerNamePrefix))
						}
					}

					return len(syncTargetsToSync) == 1 &&
						syncTargetsToSync[secondSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(secondSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond)

				logWithTimestamp("Verify that luckyluke is not known on the first synctarget")
				_, err = vwSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.EqualError(t, err, `cowboys.wildwest.dev "luckyluke" not found`)

				logWithTimestamp("Verify that luckyluke has status unchanged on the syncer view of second syncer")
				secondVirtualWorkspaceModifiedkcpCowboy, err = vwSecondSyncerClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison two", secondVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp("Verify that luckyluke has now status changed on the upstream view, since the status for the second syncer has now been promoted to upstream no syncer view status has been promoted.")
				modifiedkcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, otherWorkspace), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in prison two", modifiedkcpCowboy.Status.Result)
			},
		},
		{
			name: "Transform spec through spec-diff annotation",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(wildwestClusterName, wildwestSyncTargetName)

				t.Log("Create cowboy luckyluke")
				_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
						Annotations: map[string]string{
							"experimental.spec-diff.workload.kcp.dev/" + syncTargetKey: `[{ "op": "replace", "path": "/intent", "value": "should catch joe and averell" }]`,
						},
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
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				t.Log("Verify there is luckyluke via virtual workspace workspace request")
				kcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = ""
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe and averell"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceCowboy.Spec))
			},
		},
		{
			name: "Override summarizing rules to disable status promotion",
			work: func(t *testing.T, kubelikeSyncerVWConfig, wildwestSyncerVWConfig *rest.Config, kubelikeClusterName, wildwestClusterName logicalcluster.Name, wildwestSyncTargetName string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(wildwestClusterName, wildwestSyncTargetName)

				t.Log("Create cowboy luckyluke")
				_, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Create(logicalcluster.WithCluster(ctx, wildwestClusterName), &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
						Annotations: map[string]string{
							"experimental.summarizing.workload.kcp.dev": `[{"fieldPath": "status", "promoteToUpstream": false}]`,
						},
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
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
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
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false
					}
					require.NoError(t, err)
					syncTargetsToSync := map[string]string{}
					for name, value := range kcpCowboy.Labels {
						if strings.HasPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
							syncTargetsToSync[strings.TrimPrefix(name, workloadv1alpha1.ClusterResourceStateLabelPrefix)] = value
						}
					}

					syncTargetsWithFinalizer := sets.NewString()
					for _, name := range kcpCowboy.Finalizers {
						if strings.HasPrefix(name, shared.SyncerFinalizerNamePrefix) {
							syncTargetsWithFinalizer.Insert(strings.TrimPrefix(name, shared.SyncerFinalizerNamePrefix))
						}
					}

					return len(syncTargetsToSync) == 1 &&
						syncTargetsToSync[syncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(syncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond)

				t.Log("Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.WildwestV1alpha1().Cowboys("default").Patch(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Log("Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				t.Log("Verify that luckyluke has status unchanged on the upstream view, since the status field promotion has been disabled by annotation")
				modifiedkcpCowboy, err := wildwestClusterClient.WildwestV1alpha1().Cowboys("default").Get(logicalcluster.WithCluster(ctx, wildwestClusterName), "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)
				require.Equal(t, virtualWorkspaceModifiedkcpCowboy.ResourceVersion, modifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy.Status.Result = ""
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, modifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, modifiedkcpCowboy.Spec))
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

			t.Logf("Bind kubelike workspace")
			framework.NewBindCompute(t, kubelikeWorkspace, server,
				framework.WithAPIExportsWorkloadBindOption("root:compute:kubernetes"),
			).Bind(t)

			t.Log("Waiting for ingresses crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.Cluster(kubelikeWorkspace).NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for ingresses crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Waiting for services crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.Cluster(kubelikeWorkspace).CoreV1().Services("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for services crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Setting up an unrelated workspace with cowboys...")

			unrelatedWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

			sourceCrdClient, err := kcpapiextensionsclientset.NewForConfig(server.BaseConfig(t))
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
				// empty APIExports so we do not add global kubernetes APIExport.
				framework.WithAPIExports(""),
				framework.WithSyncTarget(wildwestWorkspace, wildwestSyncTargetName),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					// Always install the crd regardless of whether the target is
					// logical or not since cowboys is not a native type.
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err)
					t.Log("Installing test CRDs into sink cluster...")
					fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					if isFakePCluster {
						// Only need to install services in a non-logical cluster
						kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: "core.k8s.io", Resource: "services"})
					}
				}),
			).Start(t)

			t.Logf("Bind wildwest workspace")
			framework.NewBindCompute(t, wildwestWorkspace, server,
				framework.WithAPIExportsWorkloadBindOption(wildwestWorkspace.String()+":kubernetes"),
			).Bind(t)

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

func TestUpsyncerVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	var testCases = []struct {
		name string
		work func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string)
	}{
		{
			name: "list kcp resources through upsyncer virtual workspace",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Listing PVs through upsyncer virtual workspace...")
				pvs, err := kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).List(ctx, metav1.ListOptions{})
				require.NoError(t, err)

				t.Log("Checking if we can find the PV we created in the source cluster: test-pv")
				require.Len(t, pvs.Items, 1)
			},
		},
		{
			name: "create a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv2",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: "Upsync",
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/data",
							},
						},
					},
				}

				t.Log("Creating PV test-pv2 through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Log("Checking if the PV test-pv2 was created in the source cluster...")
				_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv2", metav1.GetOptions{})
				require.NoError(t, err)
			},
		},
		{
			name: "create a persistentvolume in kcp through upsyncer virtual workspace with a spec transformation",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv3",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: "Upsync",
						},
						Annotations: map[string]string{
							"spec-diff.upsync.workload.kcp.dev/" + syncTargetKey: "[{\"op\":\"replace\",\"path\":\"/capacity/storage\",\"value\":\"2Gi\"}]",
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/data",
							},
						},
					},
				}

				t.Log("Creating PV test-pv3 through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Log("Checking if the PV test-pv3 was created in the source cluster...")
				pv3, err := kubeClusterClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv3", metav1.GetOptions{})
				require.NoError(t, err)

				t.Log("Checking if the PV test-pv3 was created with the correct spec after transformation...")
				require.Equal(t, resource.MustParse("2Gi"), pv3.Spec.Capacity[corev1.ResourceStorage])
			},
		},
		{
			name: "try to create a persistentvolume in kcp through upsyncer virtual workspace, without the statelabel set to Upsync, should fail",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv2",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: "notupsync",
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/data",
							},
						},
					},
				}

				t.Log("Creating PV test-pv2 through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Create(ctx, pv, metav1.CreateOptions{})
				require.Error(t, err)
			},
		},
		{
			name: "update a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Getting PV test-pv through upsyncer virtual workspace...")
				pv, err := kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				pv.Spec.PersistentVolumeSource.HostPath.Path = "/tmp/data2"

				t.Log("Updating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Update(ctx, pv, metav1.UpdateOptions{})
				require.NoError(t, err)

				t.Log("Checking if the PV test-pv was updated in the source cluster...")
				pv, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, pv.Spec.PersistentVolumeSource.HostPath.Path, "/tmp/data2")
			},
		},
		{
			name: "update a persistentvolume in kcp through upsyncer virtual workspace, try to remove the upsync state label, expect error.",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Getting PV test-pv through upsyncer virtual workspace...")
				pv, err := kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				// Changing the label to something else, should fail.
				pv.Labels["state.workload.kcp.dev/"+syncTargetKey] = "notupsync"
				pv.Spec.PersistentVolumeSource.HostPath.Path = "/tmp/data/changed"

				t.Log("Updating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Update(ctx, pv, metav1.UpdateOptions{})
				require.Error(t, err)

				t.Log("Ensure PV test-pv is not changed...")
				pv, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				require.Equal(t, pv.Spec.PersistentVolumeSource.HostPath.Path, "/tmp/data")
			},
		},
		{
			name: "Delete a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, kubelikeSyncerVWConfig *rest.Config, kubelikeClusterName logicalcluster.Name, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(kubelikeSyncerVWConfig)
				require.NoError(t, err)

				t.Log("Creating PV test-pv3 through upsyncer virtual workspace...")
				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv3",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: "Upsync",
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/data",
							},
						},
					},
				}
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Log("Deleting PV test-pv3 through upsyncer virtual workspace...")
				err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(kubelikeClusterName).Delete(ctx, "test-pv3", metav1.DeleteOptions{})
				require.NoError(t, err)
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			framework.Suite(t, "transparent-multi-cluster")

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			kubelikeWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

			t.Logf("Deploying syncer into workspace %s", kubelikeWorkspace)
			kubelikeSyncer := framework.NewSyncerFixture(t, server, kubelikeWorkspace,
				framework.WithSyncTarget(kubelikeWorkspace, "kubelike"),
				framework.WithExtraResources("persistentvolumes"),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					if !isFakePCluster {
						// Only need to install services,ingresses and persistentvolumes in a logical cluster
						return
					}
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err, "failed to create apiextensions client")
					t.Logf("Installing test CRDs into sink cluster...")
					kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
						metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
						metav1.GroupResource{Group: "core.k8s.io", Resource: "persistentvolumes"},
					)
					require.NoError(t, err)
				}),
			).Start(t)

			t.Logf("Bind kubelike workspace")
			framework.NewBindCompute(t, kubelikeWorkspace, server,
				framework.WithAPIExportsWorkloadBindOption("root:compute:kubernetes"),
			).Bind(t)

			t.Log("Waiting for services crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().Services().Cluster(kubelikeWorkspace).Namespace("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for services crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Waiting for the persistentvolumes crd to be imported and available in the kubelike source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().PersistentVolumes().Cluster(kubelikeWorkspace).List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for persistentvolumes crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(kubelikeSyncer.SyncerConfig.SyncTargetWorkspace, kubelikeSyncer.SyncerConfig.SyncTargetName)

			t.Log("Creating a persistentvolume in the kubelike source cluster...")
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						"state.workload.kcp.dev/" + syncTargetKey: "Upsync",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/data",
						},
					},
				},
			}
			t.Logf("Creating PV %s through upsyncer virtual workspace...", pv.Name)
			_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(kubelikeWorkspace).Create(ctx, pv, metav1.CreateOptions{})
			require.NoError(t, err)

			// create virtual workspace rest configs
			rawConfig, err := server.RawConfig()
			require.NoError(t, err)
			virtualWorkspaceRawConfig := rawConfig.DeepCopy()
			virtualWorkspaceRawConfig.Clusters["kubelike"] = rawConfig.Clusters["base"].DeepCopy()
			virtualWorkspaceRawConfig.Clusters["kubelike"].Server = rawConfig.Clusters["base"].Server + "/services/upsyncer/" + kubelikeWorkspace.String() + "/" + kubelikeSyncer.SyncerConfig.SyncTargetName + "/" + kubelikeSyncer.SyncerConfig.SyncTargetUID
			virtualWorkspaceRawConfig.Contexts["kubelike"] = rawConfig.Contexts["base"].DeepCopy()
			virtualWorkspaceRawConfig.Contexts["kubelike"].Cluster = "kubelike"
			kubelikeVWConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "kubelike", nil, nil).ClientConfig()
			kubelikeVWConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(kubelikeVWConfig), t.Name()))
			require.NoError(t, err)

			t.Log("Starting test...")
			testCase.work(t, kubelikeVWConfig, kubelikeWorkspace, syncTargetKey)
		})
	}
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
