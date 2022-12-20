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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
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
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/config/rootcompute"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func deploymentsAPIResourceList(clusterName logicalcluster.Name) *metav1.APIResourceList {
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
				StorageVersionHash: discovery.StorageVersionHash(clusterName, "apps", "v1", "Deployment"),
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

func requiredCoreAPIResourceList(clusterName logicalcluster.Name) *metav1.APIResourceList {
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
				StorageVersionHash: discovery.StorageVersionHash(clusterName, "", "v1", "ConfigMap"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces",
				SingularName:       "namespace",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(clusterName, "", "v1", "Namespace"),
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
				StorageVersionHash: discovery.StorageVersionHash(clusterName, "", "v1", "Secret"),
			},
			{
				Kind:               "ServiceAccount",
				Name:               "serviceaccounts",
				SingularName:       "serviceaccount",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(clusterName, "", "v1", "ServiceAccount"),
			},
		},
	}
}

func withRootComputeAPIResourceList(workspaceName logicalcluster.Name, rootComputeLogicalCluster logicalcluster.Name) []*metav1.APIResourceList {
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
			StorageVersionHash: discovery.StorageVersionHash(rootComputeLogicalCluster, "", "v1", "Service"),
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
		deploymentsAPIResourceList(rootComputeLogicalCluster),
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
					StorageVersionHash: discovery.StorageVersionHash(rootComputeLogicalCluster, "networking.k8s.io", "v1", "Ingress"),
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

func logWithTimestamp(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	t.Logf("[%s] %s", time.Now().Format("15:04:05.000000"), fmt.Sprintf(format, args...))
}

func TestSyncerVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	server := framework.SharedKcpServer(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)
	wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	var testCases = []struct {
		name string
		work func(t *testing.T, testCaseWorkspace logicalcluster.Path)
	}{
		{
			name: "isolated API domains per syncer",
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				kubelikeLocationWorkspace := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("kubelike-locations"))

				logWithTimestamp(t, "Deploying syncer into workspace %s", kubelikeLocationWorkspace)
				kubelikeSyncer := framework.NewSyncerFixture(t, server, kubelikeLocationWorkspace,
					framework.WithSyncTargetName("kubelike"),
					framework.WithAPIExports("root:compute:kubernetes"),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						if !isFakePCluster {
							// Only need to install services and ingresses in a logical cluster
							return
						}
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err, "failed to create apiextensions client")
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
							metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
						)
						require.NoError(t, err)
					}),
				).Start(t)

				wildwestLocationWorkspace := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationWorkspace)

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationWorkspace,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				kubelikeVWDiscoverClusterClient, err := kcpdiscovery.NewForConfig(kubelikeSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				// We need to get a resource in the "root:compute" cluster to get the logical cluster name, in this case we use the
				// kubernetes APIExport, as we know that it exists.
				kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)
				export, err := kcpClusterClient.Cluster(rootcompute.RootComputeClusterName).ApisV1alpha1().APIExports().Get(context.Background(), "kubernetes", metav1.GetOptions{})
				require.NoError(t, err)
				rootComputeLogicalCluster := logicalcluster.From(export)

				logWithTimestamp(t, "Check discovery in kubelike virtual workspace")
				require.Eventually(t, func() bool {
					_, kubelikeAPIResourceLists, err := kubelikeVWDiscoverClusterClient.ServerGroupsAndResources()
					if err != nil {
						return false
					}
					return len(cmp.Diff(
						withRootComputeAPIResourceList(kubelikeLocationWorkspace, rootComputeLogicalCluster),
						sortAPIResourceList(kubelikeAPIResourceLists))) == 0
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Check discovery in wildwest virtual workspace")
				wildwestVWDiscoverClusterClient, err := kcpdiscovery.NewForConfig(wildwestSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					_, wildwestAPIResourceLists, err := wildwestVWDiscoverClusterClient.ServerGroupsAndResources()
					if err != nil {
						return false
					}
					return len(cmp.Diff([]*metav1.APIResourceList{
						requiredCoreAPIResourceList(wildwestLocationWorkspace),
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
									StorageVersionHash: discovery.StorageVersionHash(wildwestLocationWorkspace, "wildwest.dev", "v1alpha1", "Cowboy"),
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
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationClusterName)

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				logWithTimestamp(t, "Create two service accounts")
				_, err := kubeClusterClient.Cluster(wildwestLocationClusterName.Path()).CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-1",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(wildwestLocationClusterName.Path()).CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "service-account-2",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
				var token1, token2 string
				require.Eventually(t, func() bool {
					secrets, err := kubeClusterClient.Cluster(wildwestLocationClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
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

				configUser1 := framework.ConfigWithToken(token1, wildwestSyncer.SyncerVirtualWorkspaceConfig)

				configUser2 := framework.ConfigWithToken(token2, wildwestSyncer.SyncerVirtualWorkspaceConfig)

				vwClusterClientUser1, err := wildwestclientset.NewForConfig(configUser1)
				require.NoError(t, err)
				vwClusterClientUser2, err := wildwestclientset.NewForConfig(configUser2)
				require.NoError(t, err)

				logWithTimestamp(t, "Check discovery in wildwest virtual workspace with unprivileged service-account-1, expecting forbidden")
				_, err = vwClusterClientUser1.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
				require.Error(t, err)
				require.True(t, errors.IsForbidden(err))

				logWithTimestamp(t, "Giving service-account-2 permissions to access wildwest virtual workspace")
				_, err = kubeClusterClient.Cluster(wildwestLocationClusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx,
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
							Name:     "wildwest-syncer",
						},
					}, metav1.CreateOptions{},
				)
				require.NoError(t, err)
				_, err = kubeClusterClient.Cluster(wildwestLocationClusterName.Path()).RbacV1().ClusterRoles().Create(ctx,
					&rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{
							Name: "wildwest-syncer",
						},
						Rules: []rbacv1.PolicyRule{
							{
								Verbs:         []string{"sync"},
								APIGroups:     []string{"workload.kcp.dev"},
								Resources:     []string{"synctargets"},
								ResourceNames: []string{"wildwest"},
							},
						},
					}, metav1.CreateOptions{},
				)
				require.NoError(t, err)

				logWithTimestamp(t, "Check discovery in wildwest virtual workspace with unprivileged service-account-2, expecting success")
				framework.Eventually(t, func() (bool, string) {
					_, err = vwClusterClientUser2.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil, fmt.Sprintf("waiting for service-account-2 to be able to list cowboys: %v", err)
				}, wait.ForeverTestTimeout, time.Millisecond*200)

				logWithTimestamp(t, "Double check that service-account-1 still cannot access wildwest virtual workspace")
				_, err = vwClusterClientUser1.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
				require.Error(t, err)
				require.True(t, errors.IsForbidden(err))
			},
		},
		{
			name: "access kcp resources in location workspace through syncer virtual workspace ",
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationClusterName.Path())

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithSyncedUserWorkspaces(wildwestLocationClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				logWithTimestamp(t, "Bind wildwest location workspace to itself")
				framework.NewBindCompute(t, wildwestLocationClusterName.Path(), server,
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.Path().Join("kubernetes").String()),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				syncTargetKey := wildwestSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				logWithTimestamp(t, "Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				logWithTimestamp(t, "Verify there is luckyluke via virtual workspace workspace request")
				kcpCowboy, err := wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)
				require.Equal(t, kcpCowboy.Spec, virtualWorkspaceCowboy.Spec)
				require.Equal(t, kcpCowboy.Status, virtualWorkspaceCowboy.Status)

				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				logWithTimestamp(t, "Verify that luckyluke has also status changed on the upstream view, since the status field is promoted by default")
				modifiedkcpCowboy, err := wildwestClusterClient.Cluster(wildwestLocationClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				consumerClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("consumer"))

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationClusterName)

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				logWithTimestamp(t, "Bind consumer workspace to wildwest location workspace")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				syncTargetKey := wildwestSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				logWithTimestamp(t, "Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				logWithTimestamp(t, "Verify there is luckyluke via direct access")
				kcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)
				require.Empty(t, cmp.Diff(kcpCowboy.Spec, virtualWorkspaceCowboy.Spec))
				require.Empty(t, cmp.Diff(kcpCowboy.Status, virtualWorkspaceCowboy.Status))

				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				logWithTimestamp(t, "Verify that luckyluke has also status changed on the upstream view, since the status field is promoted by default")
				modifiedkcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				consumerClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("consumer"))

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying north syncer into workspace %s", wildwestLocationClusterName)

				wildwestNorthSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest-north"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "wildwest-north", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"north"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Deploying south syncer into workspace %s", wildwestLocationClusterName)
				wildwestSouthSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest-south"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "wildwest-south", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"south"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Create 2 locations, one for each SyncTarget")
				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "firstlocation",
						Labels: map[string]string{
							"region": "north",
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"region": "north",
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

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "secondlocation",
						Labels: map[string]string{
							"region": "south",
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"region": "south",
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

				logWithTimestamp(t, "Create 2 placements, one for each SyncTarget")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithPlacementNameBindOption("north"),
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"region": "north",
						},
					}),
				).Bind(t)

				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithPlacementNameBindOption("south"),
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"region": "south",
						},
					}),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "ERROR: Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				vwNorthClusterClient, err := wildwestclientset.NewForConfig(wildwestNorthSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				vwSouthClusterClient, err := wildwestclientset.NewForConfig(wildwestSouthSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				logWithTimestamp(t, "Wait until the north virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwNorthClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait until the south virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSouthClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy on the 2 synctargets")
				var kcpCowboy *wildwestv1alpha1.Cowboy
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for the 2 syncers to own lukyluke")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via north virtual workspace to report in status that joe is in northern prison")
				_, err = vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in northern prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Patch luckyluke via second virtual workspace to report in status that joe is in southern prison")
				_, err = vwSouthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in southern prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has status changed on the syncer view of north syncer")
				northVirtualWorkspaceModifiedkcpCowboy, err := vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in northern prison", northVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has status changed on the syncer view of south syncer")
				southVirtualWorkspaceModifiedkcpCowboy, err := vwSouthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in southern prison", southVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has status unchanged on the upstream view, since the status field is never promoted when a resource is scheduled to 2 synctargets")
				modifiedkcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.Status, modifiedkcpCowboy.Status)
			},
		},
		{
			name: "Correctly manage status, with promote and unpromote, when moving a cowboy from one synctarget to the other",
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				consumerClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("consumer"))

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying north syncer into workspace %s", wildwestLocationClusterName)

				wildwestNorthSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest-north"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "wildwest-north", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"north"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				northSyncTargetKey := wildwestNorthSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Deploying south syncer into workspace %s", wildwestLocationClusterName.Path())
				wildwestSouthSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest-south"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "wildwest-south", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"south"}]`), metav1.PatchOptions{})
				require.NoError(t, err)

				southSyncTargetKey := wildwestSouthSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Delete default location")
				err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).SchedulingV1alpha1().Locations().Delete(ctx, "default", metav1.DeleteOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Create 2 locations, one for each SyncTarget")
				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "firstlocation",
						Labels: map[string]string{
							"region": "north",
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"region": "north",
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

				_, err = kcpClusterClient.Cluster(wildwestLocationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, &schedulingv1alpha1.Location{
					ObjectMeta: metav1.ObjectMeta{
						Name: "secondlocation",
						Labels: map[string]string{
							"region": "south",
						},
					},
					Spec: schedulingv1alpha1.LocationSpec{
						InstanceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"region": "south",
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

				logWithTimestamp(t, "Using User workspace: %s", consumerClusterName.String())

				logWithTimestamp(t, "Create the north placement, for the north SyncTarget")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithPlacementNameBindOption("north"),
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"region": "north",
						},
					}),
				).Bind(t)

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "ERROR: Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				vwNorthClusterClient, err := wildwestclientset.NewForConfig(wildwestNorthSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)
				vwSouthClusterClient, err := wildwestclientset.NewForConfig(wildwestSouthSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Wait until the north virtual workspace has the resource type")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwNorthClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait until the south virtual workspace has the resource type")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwSouthClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: wildwestv1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy on the north synctarget, and for the syncer to own it")
				var kcpCowboy *wildwestv1alpha1.Cowboy
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
						syncTargetsToSync[northSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(northSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via north virtual workspace to report in status that joe is in northern prison")
				_, err = vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in northern prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has status changed on the syncer view of north syncer")
				northVirtualWorkspaceModifiedkcpCowboy, err := vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in northern prison", northVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has also status changed on the upstream view, since the status field is promoted when scheduled on only one synctarget")
				modifiedkcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in northern prison", modifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Create the south placement, for the south SyncTarget")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithPlacementNameBindOption("south"),
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
					framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
						MatchLabels: map[string]string{
							"region": "south",
						},
					}),
				).Bind(t)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy on the 2 synctargets, and for both syncers to own it")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
						syncTargetsToSync[northSyncTargetKey] == "Sync" &&
						syncTargetsToSync[southSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 2 &&
						syncTargetsWithFinalizer.Has(northSyncTargetKey) &&
						syncTargetsWithFinalizer.Has(southSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via south virtual workspace to report in status that joe is in southern prison")
				_, err = vwSouthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in southern prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has status unchanged on the syncer view of the north syncer")
				northVirtualWorkspaceModifiedkcpCowboy, err = vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in northern prison", northVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has status changed on the syncer view of south syncer")
				southVirtualWorkspaceModifiedkcpCowboy, err := vwSouthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in southern prison", southVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has status unchanged on the upstream view, since no syncer view status has been promoted since the last promotion, because scheduled on 2 synctargets")
				modifiedkcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in northern prison", modifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Remove the placement for the north SyncTarget")
				err = kcpClusterClient.Cluster(consumerClusterName.Path()).SchedulingV1alpha1().Placements().Delete(ctx, "north", metav1.DeleteOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy on the south synctarget only, and for the south syncer only to own it")
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
						syncTargetsToSync[southSyncTargetKey] == "Sync" &&
						syncTargetsWithFinalizer.Len() == 1 &&
						syncTargetsWithFinalizer.Has(southSyncTargetKey)
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Verify that luckyluke is not known on the north synctarget")
				_, err = vwNorthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.EqualError(t, err, `cowboys.wildwest.dev "luckyluke" not found`)

				logWithTimestamp(t, "Verify that luckyluke has status unchanged on the syncer view of south syncer")
				southVirtualWorkspaceModifiedkcpCowboy, err = vwSouthClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in southern prison", southVirtualWorkspaceModifiedkcpCowboy.Status.Result)

				logWithTimestamp(t, "Verify that luckyluke has now status changed on the upstream view, since the status for the south syncer has now been promoted to upstream.")
				modifiedkcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, "joe in southern prison", modifiedkcpCowboy.Status.Result)
			},
		},
		{
			name: "Transform spec through spec-diff annotation",
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				consumerClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("consumer"))

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationClusterName)

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				logWithTimestamp(t, "Bind consumer workspace to wildwest location workspace")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
				).Bind(t)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				syncTargetKey := wildwestSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
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

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				logWithTimestamp(t, "Verify there is luckyluke via direct access")
				kcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
			work: func(t *testing.T, testCaseWorkspace logicalcluster.Path) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err)

				consumerClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("consumer"))

				wildwestLocationClusterName := framework.NewWorkspaceFixture(t, server, testCaseWorkspace, framework.WithName("wildwest-locations"))
				logWithTimestamp(t, "Deploying syncer into workspace %s", wildwestLocationClusterName)

				wildwestSyncer := framework.NewSyncerFixture(t, server, wildwestLocationClusterName,
					framework.WithExtraResources("cowboys.wildwest.dev"),
					// empty APIExports so we do not add global kubernetes APIExport.
					framework.WithAPIExports(""),
					framework.WithSyncTargetName("wildwest"),
					framework.WithSyncedUserWorkspaces(consumerClusterName),
					framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
						// Always install the crd regardless of whether the target is
						// logical or not since cowboys is not a native type.
						sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
						require.NoError(t, err)
						logWithTimestamp(t, "Installing test CRDs into sink cluster...")
						fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
					}),
				).Start(t)

				logWithTimestamp(t, "Bind consumer workspace to wildwest location workspace")
				framework.NewBindCompute(t, consumerClusterName.Path(), server,
					framework.WithAPIExportsWorkloadBindOption(wildwestLocationClusterName.String()+":kubernetes"),
					framework.WithLocationWorkspaceWorkloadBindOption(wildwestLocationClusterName.Path()),
				).Bind(t)

				syncTargetKey := wildwestSyncer.ToSyncTargetKey()

				logWithTimestamp(t, "Wait for being able to list cowboys in the consumer workspace via direct access")
				require.Eventually(t, func() bool {
					_, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					if errors.IsNotFound(err) {
						return false
					} else if err != nil {
						logWithTimestamp(t, "Failed to list Cowboys: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Create cowboy luckyluke")
				_, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
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

				vwClusterClient, err := wildwestclientset.NewForConfig(wildwestSyncer.SyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				logWithTimestamp(t, "Wait until the virtual workspace has the cowboy resource type")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Wait for resource controller to schedule cowboy and then show up via virtual workspace wildcard request")
				var cowboys *wildwestv1alpha1.CowboyList
				require.Eventually(t, func() bool {
					cowboys, err = vwClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
					require.LessOrEqual(t, len(cowboys.Items), 1, "expected no other cowboy than luckyluke, got %d cowboys.", len(cowboys.Items))
					return len(cowboys.Items) == 1
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				require.Equal(t, "luckyluke", cowboys.Items[0].Name)

				logWithTimestamp(t, "Verify there is luckyluke via direct access and through virtual workspace")
				kcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, kcpCowboy.UID, virtualWorkspaceCowboy.UID)
				require.Equal(t, kcpCowboy.Spec, virtualWorkspaceCowboy.Spec)
				require.Equal(t, kcpCowboy.Status, virtualWorkspaceCowboy.Status)
				require.Eventually(t, func() bool {
					kcpCowboy, err = wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				logWithTimestamp(t, "Patch luckyluke via virtual workspace to catch averell")
				_, err = vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Verify that luckyluke has only status changed on the syncer view, since the spec.intent field is not part of summarized fields")
				virtualWorkspaceModifiedkcpCowboy, err := vwClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEqual(t, kcpCowboy.ResourceVersion, virtualWorkspaceModifiedkcpCowboy.ResourceVersion)

				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch joe"
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Status, virtualWorkspaceModifiedkcpCowboy.Status))
				require.Empty(t, cmp.Diff(expectedModifiedKcpCowboy.Spec, virtualWorkspaceModifiedkcpCowboy.Spec))

				logWithTimestamp(t, "Verify that luckyluke has status unchanged on the upstream view, since the status field promotion has been disabled by annotation")
				modifiedkcpCowboy, err := wildwestClusterClient.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
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

			orgClusterName := framework.NewOrganizationFixture(t, server)

			testCase.work(t, orgClusterName.Path())
		})
	}
}

func TestUpsyncerVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	server := framework.SharedKcpServer(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	var testCases = []struct {
		name string
		work func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string)
	}{
		{
			name: "list kcp resources through upsyncer virtual workspace",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

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
				logWithTimestamp(t, "Creating PV %s through direct kube client ...", pv.Name)
				_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Listing PVs through upsyncer virtual workspace...")
				pvs, err := kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).List(ctx, metav1.ListOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Checking if we can find the PV we created in the source cluster: test-pv")
				require.Len(t, pvs.Items, 1)
			},
		},
		{
			name: "create a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
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

				logWithTimestamp(t, "Creating PV test-pv through upsyncer virtual workspace...")
				pv, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)
				require.Empty(t, pv.Status)

				logWithTimestamp(t, "Updating status of the PV test-pv through upsyncer virtual workspace...")
				pv.Status.Phase = corev1.VolumeAvailable
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).UpdateStatus(ctx, pv, metav1.UpdateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Checking if the PV test-pv was created in the source cluster...")
				pv, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, corev1.VolumeAvailable, pv.Status.Phase)
			},
		},
		{
			name: "create a persistentvolume in kcp through upsyncer virtual workspace with a resource transformation",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: "Upsync",
						},
						Annotations: map[string]string{
							"internal.workload.kcp.dev/upsyncdiff" + syncTargetKey: "[{\"op\":\"replace\",\"path\":\"/spec/capacity/storage\",\"value\":\"2Gi\"}]",
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

				logWithTimestamp(t, "Creating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Checking if the PV test-pv4 was created in the source cluster...")
				pvCreated, err := kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Checking if the PV test-pv was created with the correct values after transformation...")
				require.Equal(t, resource.MustParse("2Gi"), pvCreated.Spec.Capacity[corev1.ResourceStorage])
			},
		},
		{
			name: "try to create a persistentvolume in kcp through upsyncer virtual workspace, without the statelabel set to Upsync, should fail",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
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

				logWithTimestamp(t, "Creating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.Error(t, err)
			},
		},
		{
			name: "update a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Creating a persistentvolume in the kubelike source cluster...")
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
				logWithTimestamp(t, "Creating PV %s through direct kube client ...", pv.Name)
				_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Getting PV test-pv through upsyncer virtual workspace...")
				pv, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				pv.Spec.PersistentVolumeSource.HostPath.Path = "/tmp/data2"

				logWithTimestamp(t, "Updating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Update(ctx, pv, metav1.UpdateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Checking if the PV test-pv was updated in the source cluster...")
				pv, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, pv.Spec.PersistentVolumeSource.HostPath.Path, "/tmp/data2")
			},
		},
		{
			name: "update a persistentvolume in kcp through upsyncer virtual workspace, try to remove the upsync state label, expect error.",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Creating a persistentvolume in the kubelike source cluster...")
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
				logWithTimestamp(t, "Creating PV %s through direct kube client ...", pv.Name)
				_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Getting PV test-pv through upsyncer virtual workspace...")
				pv, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				// Changing the label to something else, should fail.
				pv.Labels["state.workload.kcp.dev/"+syncTargetKey] = "notupsync"
				pv.Spec.PersistentVolumeSource.HostPath.Path = "/tmp/data/changed"

				logWithTimestamp(t, "Updating PV test-pv through upsyncer virtual workspace...")
				_, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Update(ctx, pv, metav1.UpdateOptions{})
				require.Error(t, err)

				logWithTimestamp(t, "Ensure PV test-pv is not changed...")
				pv, err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Get(ctx, "test-pv", metav1.GetOptions{})
				require.NoError(t, err)

				require.Equal(t, pv.Spec.PersistentVolumeSource.HostPath.Path, "/tmp/data")
			},
		},
		{
			name: "Delete a persistentvolume in kcp through upsyncer virtual workspace",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, workspaceName logicalcluster.Path, syncTargetKey string) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				kubelikeSyncerVWClient, err := kcpkubernetesclientset.NewForConfig(syncer.UpsyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				logWithTimestamp(t, "Creating a persistentvolume in the kubelike source cluster...")
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
				logWithTimestamp(t, "Creating PV %s through direct kube client ...", pv.Name)
				_, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Create(ctx, pv, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestamp(t, "Deleting PV test-pv3 through upsyncer virtual workspace...")
				err = kubelikeSyncerVWClient.CoreV1().PersistentVolumes().Cluster(workspaceName).Delete(ctx, "test-pv", metav1.DeleteOptions{})
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

			orgClusterName := framework.NewOrganizationFixture(t, server)

			upsyncerClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("upsyncer"))

			logWithTimestamp(t, "Deploying syncer into workspace %s", upsyncerClusterName)
			upsyncer := framework.NewSyncerFixture(t, server, upsyncerClusterName,
				framework.WithSyncTargetName("upsyncer"),
				framework.WithExtraResources("persistentvolumes"),
				framework.WithAPIExports(""),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					if !isFakePCluster {
						// Only need to install services,ingresses and persistentvolumes in a logical cluster
						return
					}
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err, "failed to create apiextensions client")
					logWithTimestamp(t, "Installing test CRDs into sink cluster...")
					kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
						metav1.GroupResource{Group: "core.k8s.io", Resource: "persistentvolumes"},
					)
					require.NoError(t, err)
				}),
			).Start(t)

			logWithTimestamp(t, "Bind upsyncer workspace")
			framework.NewBindCompute(t, upsyncerClusterName.Path(), server,
				framework.WithAPIExportsWorkloadBindOption(upsyncerClusterName.String()+":kubernetes"),
			).Bind(t)

			logWithTimestamp(t, "Waiting for the persistentvolumes crd to be imported and available in the upsyncer source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().PersistentVolumes().Cluster(upsyncerClusterName.Path()).List(ctx, metav1.ListOptions{})
				if err != nil {
					logWithTimestamp(t, "error seen waiting for persistentvolumes crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			syncTargetKey := upsyncer.ToSyncTargetKey()

			logWithTimestamp(t, "Starting test...")
			testCase.work(t, upsyncer, upsyncerClusterName.Path(), syncTargetKey)
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
