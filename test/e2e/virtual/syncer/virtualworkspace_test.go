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
	"sort"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func deploymentsAPIResourceList(workspaceName string) *metav1.APIResourceList {
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
				Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "apps", "v1", "Deployment"),
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

func requiredCoreAPIResourceList(workspaceName string) *metav1.APIResourceList {
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
				Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "ConfigMap"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces",
				SingularName:       "namespace",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
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
				Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(workspaceName, "", "v1", "Secret"),
			},
			{
				Kind:               "ServiceAccount",
				Name:               "serviceaccounts",
				SingularName:       "serviceaccount",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
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

	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, kubelikeWorkspaceName, wildwestWorkspaceName string, kubelikeWorkspaceClient kubernetesclientset.Interface, wildwestWorkspaceClient wildwestclientset.Interface, kubelikeSyncerVrtualWorkspaceConfig, wildwestSyncerVrtualWorkspaceConfig *rest.Config)
	}{
		{
			name: "isolated API domains per syncer",
			work: func(ctx context.Context, t *testing.T, kubelikeWorkspaceName, wildwestWorkspaceName string, _ kubernetesclientset.Interface, _ wildwestclientset.Interface, kubelikeSyncerVirtualWorkspaceConfig, wildwestSyncerVirtualWorkspaceConfig *rest.Config) {
				kubelikeVWDiscoverClient, err := clientgodiscovery.NewDiscoveryClientForConfig(kubelikeSyncerVirtualWorkspaceConfig)
				require.NoError(t, err)
				_, kubelikeAPIResourceLists, err := kubelikeVWDiscoverClient.ServerGroupsAndResources()
				require.NoError(t, err)
				require.Equal(t, toYAML(t, []*metav1.APIResourceList{
					deploymentsAPIResourceList(kubelikeWorkspaceName),
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
								Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
								ShortNames:         []string{"ing"},
								StorageVersionHash: discovery.StorageVersionHash(kubelikeWorkspaceName, "networking.k8s.io", "v1", "Ingress"),
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
						requiredCoreAPIResourceList(kubelikeWorkspaceName),
						metav1.APIResource{
							Kind:               "Service",
							Name:               "services",
							SingularName:       "service",
							Namespaced:         true,
							Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
							StorageVersionHash: discovery.StorageVersionHash(kubelikeWorkspaceName, "", "v1", "Service"),
						},
						metav1.APIResource{
							Kind:               "Service",
							Name:               "services/status",
							SingularName:       "",
							Namespaced:         true,
							Verbs:              metav1.Verbs{"get", "patch", "update"},
							StorageVersionHash: "",
						}),
				}), toYAML(t, sortAPIResourceList(kubelikeAPIResourceLists)))

				wildwestVWDiscoverClient, err := clientgodiscovery.NewDiscoveryClientForConfig(wildwestSyncerVirtualWorkspaceConfig)
				require.NoError(t, err)
				_, wildwestAPIResourceLists, err := wildwestVWDiscoverClient.ServerGroupsAndResources()
				require.NoError(t, err)
				require.Equal(t, toYAML(t, []*metav1.APIResourceList{
					deploymentsAPIResourceList(wildwestWorkspaceName),
					requiredCoreAPIResourceList(wildwestWorkspaceName),
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
								Verbs:              metav1.Verbs{"get", "list", "patch", "create", "update", "watch"},
								StorageVersionHash: discovery.StorageVersionHash(wildwestWorkspaceName, "wildwest.dev", "v1alpha1", "Cowboy"),
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
				}), toYAML(t, sortAPIResourceList(wildwestAPIResourceLists)))
			},
		},
		{
			name: "access kcp resources through syncer virtual workspace",
			work: func(ctx context.Context, t *testing.T, kubelikeWorkspaceName, wildwestWorkspaceName string, kubelikeWorkspaceClient kubernetesclientset.Interface, wildwestWorkspaceClient wildwestclientset.Interface, kubelikeSyncerVirtualWorkspaceConfig, wildwestSyncerVirtualWorkspaceConfig *rest.Config) {
				t.Log("Create cowboy luckyluke")
				_, err := wildwestWorkspaceClient.WildwestV1alpha1().Cowboys("default").Create(ctx, &v1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "luckyluke",
					},
					Spec: v1alpha1.CowboySpec{
						Intent: "should catch joe",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				virtualWorkspaceClusterClient, err := wildwestclientset.NewClusterForConfig(wildwestSyncerVirtualWorkspaceConfig)
				require.NoError(t, err)

				t.Log("Verify there is one cowboy via direct access")
				kcpCowboys, err := wildwestWorkspaceClient.WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, kcpCowboys.Items, 1)

				t.Log("Wait until the virtual workspace has the resource")
				require.Eventually(t, func() bool {
					// resources show up asynchronously, so we have to try until List works. Then it should return all object immediately.
					_, err := virtualWorkspaceClusterClient.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Verify there is one cowboy via virtual workspace")
				virtualWorkspaceCowboys, err := virtualWorkspaceClusterClient.Cluster(logicalcluster.Wildcard).WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, virtualWorkspaceCowboys.Items, 1)
				require.Equal(t, kcpCowboys.Items[0], virtualWorkspaceCowboys.Items[0])

				t.Log("Verify there is luckyluke via virtual workspace")
				kcpCowboy, err := wildwestWorkspaceClient.WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				virtualWorkspaceCowboy, err := virtualWorkspaceClusterClient.Cluster(logicalcluster.New(wildwestWorkspaceName)).WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, *kcpCowboy, *virtualWorkspaceCowboy)

				t.Log("Patch luckyluke via virtual workspace to report in status that joe is in prison")
				_, err = virtualWorkspaceClusterClient.Cluster(logicalcluster.New(wildwestWorkspaceName)).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"status\":{\"result\":\"joe in prison\"}}"), metav1.PatchOptions{}, "status")
				require.NoError(t, err)

				t.Log("Patch luckyluke via virtual workspace to catch averell")
				_, err = virtualWorkspaceClusterClient.Cluster(logicalcluster.New(wildwestWorkspaceName)).WildwestV1alpha1().Cowboys("default").Patch(ctx, "luckyluke", types.MergePatchType, []byte("{\"spec\":{\"intent\":\"should catch averell\"}}"), metav1.PatchOptions{})
				require.NoError(t, err)

				t.Log("Verify that luckyluke has both spec and status changed")
				modifiedkcpCowboy, err := wildwestWorkspaceClient.WildwestV1alpha1().Cowboys("default").Get(ctx, "luckyluke", metav1.GetOptions{})
				require.NoError(t, err)
				expectedModifiedKcpCowboy := kcpCowboy.DeepCopy()
				expectedModifiedKcpCowboy.Status.Result = "joe in prison"
				expectedModifiedKcpCowboy.Spec.Intent = "should catch averell"
				require.NotEqual(t, expectedModifiedKcpCowboy.ResourceVersion, modifiedkcpCowboy)

				t.Log("Verify resource version, managed fields and generation")
				expectedModifiedKcpCowboy.ResourceVersion = modifiedkcpCowboy.ResourceVersion
				require.NotEqual(t, expectedModifiedKcpCowboy.ManagedFields, modifiedkcpCowboy.ManagedFields)
				expectedModifiedKcpCowboy.ManagedFields = modifiedkcpCowboy.ManagedFields
				require.Equal(t, expectedModifiedKcpCowboy.Generation+1, modifiedkcpCowboy.Generation)
				expectedModifiedKcpCowboy.Generation = modifiedkcpCowboy.Generation
				require.Equal(t, *expectedModifiedKcpCowboy, *modifiedkcpCowboy)
			},
		},
	}

	source := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, source)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			sourceConfig := source.DefaultConfig(t)
			rawConfig, err := source.RawConfig()
			require.NoError(t, err)

			sourceKubeClusterClient, err := kubernetesclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			kubelikeWorkspace := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")
			kubelikeWorkspaceClient := sourceKubeClusterClient.Cluster(kubelikeWorkspace)

			_ = framework.SyncerFixture{
				ResourcesToSync:      sets.NewString("ingresses.networking.k8s.io", "services"),
				UpstreamServer:       source,
				WorkspaceClusterName: kubelikeWorkspace,
				WorkloadClusterName:  "kubelike",
				InstallCRDs: func(config *rest.Config, isLogicalCluster bool) {
					if !isLogicalCluster {
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
				},
			}.Start(t)

			t.Log("Waiting for ingresses crd to be imported and available in the source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubelikeWorkspaceClient.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for ingresses crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Waiting for services crd to be imported and available in the source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubelikeWorkspaceClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for services crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			sourceWildwestClusterClient, err := wildwestclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			wildwestWorkspace := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")
			wildwestWorkspaceClient := sourceWildwestClusterClient.Cluster(wildwestWorkspace)

			_ = framework.SyncerFixture{
				ResourcesToSync:      sets.NewString("cowboys.wildwest.dev"),
				UpstreamServer:       source,
				WorkspaceClusterName: wildwestWorkspace,
				WorkloadClusterName:  "wildwest",
				InstallCRDs: func(config *rest.Config, isLogicalCluster bool) {
					// Always install the crd regardless of whether the target is
					// logical or not since cowboys is not a native type.
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err)
					t.Log("Installing test CRDs into sink cluster...")
					fixturewildwest.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
				},
			}.Start(t)

			t.Log("Waiting for cowboys crd to be imported and available in the source cluster...")
			require.Eventually(t, func() bool {
				_, err := wildwestWorkspaceClient.WildwestV1alpha1().Cowboys("").List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("error seen waiting for ingresses crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			adminCluster := rawConfig.Clusters["system:admin"]
			adminContext := rawConfig.Contexts["system:admin"]
			virtualWorkspaceRawConfig := rawConfig.DeepCopy()

			virtualWorkspaceRawConfig.Clusters["kubelike"] = adminCluster.DeepCopy()
			virtualWorkspaceRawConfig.Clusters["kubelike"].Server = adminCluster.Server + "/services/syncer/" + kubelikeWorkspace.String() + "/kubelike"
			virtualWorkspaceRawConfig.Contexts["kubelike"] = adminContext.DeepCopy()
			virtualWorkspaceRawConfig.Contexts["kubelike"].Cluster = "kubelike"

			virtualWorkspaceRawConfig.Clusters["wildwest"] = adminCluster.DeepCopy()
			virtualWorkspaceRawConfig.Clusters["wildwest"].Server = adminCluster.Server + "/services/syncer/" + wildwestWorkspace.String() + "/wildwest"
			virtualWorkspaceRawConfig.Contexts["wildwest"] = adminContext.DeepCopy()
			virtualWorkspaceRawConfig.Contexts["wildwest"].Cluster = "wildwest"

			kubelikeVirtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "kubelike", nil, nil).ClientConfig()
			require.NoError(t, err)
			wildwestVirtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "wildwest", nil, nil).ClientConfig()
			require.NoError(t, err)

			_, err = sourceKubeClusterClient.Cluster(kubelikeWorkspace).CoreV1().Namespaces().Patch(ctx, "default", types.StrategicMergePatchType, []byte("{\"metadata\":{\"labels\":{\"experimental.workloads.kcp.dev/scheduling-disabled\":\"true\"}}}"), metav1.PatchOptions{})
			require.NoError(t, err)

			_, err = sourceKubeClusterClient.Cluster(wildwestWorkspace).CoreV1().Namespaces().Patch(ctx, "default", types.StrategicMergePatchType, []byte("{\"metadata\":{\"labels\":{\"experimental.workloads.kcp.dev/scheduling-disabled\":\"true\"}}}"), metav1.PatchOptions{})
			require.NoError(t, err)

			t.Log("Starting test...")
			testCase.work(ctx, t, kubelikeWorkspace.String(), wildwestWorkspace.String(), kubelikeWorkspaceClient, wildwestWorkspaceClient, kubelikeVirtualWorkspaceConfig, wildwestVirtualWorkspaceConfig)
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

func toYAML(t *testing.T, obj interface{}) string {
	yml, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(yml)
}
