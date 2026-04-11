/*
Copyright 2026 The kcp Authors.

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

package garbagecollector

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestGarbageCollectorTypesFromBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	apiProviderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-api-export"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	discoveryClusterClient, err := kcpdiscovery.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Create the cowboy APIResourceSchema")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClusterClient.Cluster(apiProviderPath)))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(apiProviderPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
	_, err = kcpClusterClient.Cluster(apiProviderPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// Test multiple workspaces in parallel
	for i := range 3 {
		t.Run(fmt.Sprintf("tc%d", i), func(t *testing.T) {
			t.Parallel()

			userPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-api-binding-%d", i))

			t.Logf("Create a binding in the user workspace")
			binding := &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys",
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: apiProviderPath.String(),
							Name: cowboysAPIExport.Name,
						},
					},
				},
			}

			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err, "error creating kube cluster client")

			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "error creating kcp cluster client")

			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(userPath).ApisV1alpha2().APIBindings().Create(t.Context(), binding, metav1.CreateOptions{})
				return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating APIBinding")

			t.Logf("Wait for the binding to be ready")
			kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
				return kcpClusterClient.Cluster(userPath).ApisV1alpha2().APIBindings().Get(t.Context(), binding.Name, metav1.GetOptions{})
			}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

			wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
			require.NoError(t, err, "failed to construct wildwest cluster client for server")

			t.Logf("Wait for being able to list cowboys in the user workspace")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err := wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("").
					List(t.Context(), metav1.ListOptions{})
				if err != nil {
					return false, fmt.Sprintf("Failed to list cowboys: %v", err)
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("Creating owner cowboy")
			owner, err := wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
				Create(t.Context(),
					&wildwestv1alpha1.Cowboy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "owner",
						},
					},
					metav1.CreateOptions{})
			require.NoError(t, err, "Error creating owner cowboy %s|default/owner", userPath)

			t.Logf("Creating owned configmap")
			ownedConfigMap, err := kubeClusterClient.Cluster(userPath).CoreV1().ConfigMaps("default").Apply(t.Context(),
				corev1ac.ConfigMap("owned", "default").
					WithOwnerReferences(metav1ac.OwnerReference().
						WithAPIVersion(wildwestv1alpha1.SchemeGroupVersion.String()).
						WithKind("Cowboy").
						WithName(owner.Name).
						WithUID(owner.UID)),
				metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
			require.NoError(t, err, "Error applying owned configmap %s|default/owned", userPath)

			t.Logf("Creating owned cowboy")
			ownedCowboy, err := wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
				Create(t.Context(),
					&wildwestv1alpha1.Cowboy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "owned",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: wildwestv1alpha1.SchemeGroupVersion.String(),
									Kind:       "Cowboy",
									Name:       owner.Name,
									UID:        owner.UID,
								},
							},
						},
					},
					metav1.CreateOptions{})
			require.NoError(t, err, "Error creating owned cowboy %s|default/owner", userPath)

			t.Logf("Deleting owner cowboy")
			err = wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
				Delete(t.Context(), owner.Name, metav1.DeleteOptions{})

			t.Logf("Waiting for the owned configmap to be garbage collected")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = kubeClusterClient.Cluster(userPath).CoreV1().ConfigMaps("default").
					Get(t.Context(), ownedConfigMap.Name, metav1.GetOptions{})
				return apierrors.IsNotFound(err), fmt.Sprintf("configmap not garbage collected: %s", ownedConfigMap.Name)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmap to be garbage collected")

			t.Logf("Waiting for the owned cowboy to be garbage collected")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
					Get(t.Context(), ownedCowboy.Name, metav1.GetOptions{})
				return apierrors.IsNotFound(err), fmt.Sprintf("cowboy not garbage collected: %s", ownedConfigMap.Name)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned cowboy to be garbage collected")
		})
	}
}
