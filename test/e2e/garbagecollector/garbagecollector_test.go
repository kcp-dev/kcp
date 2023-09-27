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

package garbagecollector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestGarbageCollectorBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	wsPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-builtins"))

	t.Logf("Creating owner configmap")
	owner, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(ctx,
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owner configmap %s|default/owner", wsPath)

	t.Logf("Creating owned configmap")
	owned, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(ctx,
		corev1ac.ConfigMap("owned", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").
				WithKind("ConfigMap").
				WithName(owner.Name).
				WithUID(owner.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owned configmap %s|default/owned", wsPath)

	t.Logf("Deleting owner configmap")
	err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Delete(ctx, owner.Name, metav1.DeleteOptions{})

	t.Logf("Waiting for the owned configmap to be garbage collected")
	framework.Eventually(t, func() (bool, string) {
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Get(ctx, owned.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("configmap not garbage collected: %s", owned.Name)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmap to be garbage collected")
}

func TestGarbageCollectorTypesFromBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	apiProviderPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-api-export"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	discoveryClusterClient, err := kcpdiscovery.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Create the cowboy APIResourceSchema")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClusterClient.Cluster(apiProviderPath)))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(apiProviderPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
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
	_, err = kcpClusterClient.Cluster(apiProviderPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// Test multiple workspaces in parallel
	for i := 0; i < 3; i++ {
		i := i
		t.Run(fmt.Sprintf("tc%d", i), func(t *testing.T) {
			t.Parallel()

			c, cancelFunc := context.WithCancel(ctx)
			t.Cleanup(cancelFunc)

			userPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-api-binding-%d", i))

			t.Logf("Create a binding in the user workspace")
			binding := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
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

			framework.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(userPath).ApisV1alpha1().APIBindings().Create(c, binding, metav1.CreateOptions{})
				return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating APIBinding")

			t.Logf("Wait for the binding to be ready")
			framework.EventuallyCondition(t, func() (conditions.Getter, error) {
				return kcpClusterClient.Cluster(userPath).ApisV1alpha1().APIBindings().Get(c, binding.Name, metav1.GetOptions{})
			}, framework.Is(apisv1alpha1.InitialBindingCompleted))

			wildwestClusterClient, err := wildwestclientset.NewForConfig(server.BaseConfig(t))
			require.NoError(t, err, "failed to construct wildwest cluster client for server")

			t.Logf("Wait for being able to list cowboys in the user workspace")
			framework.Eventually(t, func() (bool, string) {
				_, err := wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("").
					List(c, metav1.ListOptions{})
				if err != nil {
					return false, fmt.Sprintf("Failed to list cowboys: %v", err)
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Logf("Creating owner cowboy")
			owner, err := wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
				Create(ctx,
					&wildwestv1alpha1.Cowboy{
						ObjectMeta: metav1.ObjectMeta{
							Name: "owner",
						},
					},
					metav1.CreateOptions{})
			require.NoError(t, err, "Error creating owner cowboy %s|default/owner", userPath)

			t.Logf("Creating owned configmap")
			ownedConfigMap, err := kubeClusterClient.Cluster(userPath).CoreV1().ConfigMaps("default").Apply(ctx,
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
				Create(ctx,
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
				Delete(ctx, owner.Name, metav1.DeleteOptions{})

			t.Logf("Waiting for the owned configmap to be garbage collected")
			framework.Eventually(t, func() (bool, string) {
				_, err = kubeClusterClient.Cluster(userPath).CoreV1().ConfigMaps("default").
					Get(ctx, ownedConfigMap.Name, metav1.GetOptions{})
				return apierrors.IsNotFound(err), fmt.Sprintf("configmap not garbage collected: %s", ownedConfigMap.Name)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmap to be garbage collected")

			t.Logf("Waiting for the owned cowboy to be garbage collected")
			framework.Eventually(t, func() (bool, string) {
				_, err = wildwestClusterClient.Cluster(userPath).WildwestV1alpha1().Cowboys("default").
					Get(ctx, ownedCowboy.Name, metav1.GetOptions{})
				return apierrors.IsNotFound(err), fmt.Sprintf("cowboy not garbage collected: %s", ownedConfigMap.Name)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned cowboy to be garbage collected")
		})
	}
}

func TestGarbageCollectorNormalCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	group := framework.UniqueGroup(".io")

	sheriffCRD1 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "one")
	sheriffCRD2 := apifixtures.NewSheriffsCRDWithSchemaDescription(group, "two")

	ws1Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-crd-1"))
	ws2Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-crd-2"))

	t.Logf("Install a normal sheriffs CRD into workspace 1 %q", ws1Path)
	bootstrapCRD(t, ws1Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD1)

	t.Logf("Install another normal sheriffs CRD with a different schema into workspace 2 %q", ws2Path)
	bootstrapCRD(t, ws2Path, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD2)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	// Test with 2 workspaces to make sure GC works for both
	workspaces := []logicalcluster.Path{ws1Path, ws2Path}
	for _, wsPath := range workspaces {
		t.Logf("Creating owner sheriff")
		owner, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVR).Namespace("default").
			Create(ctx, &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": sheriffsGVR.GroupVersion().String(),
					"kind":       "Sheriff",
					"metadata": map[string]interface{}{
						"name": "owner",
					},
				},
			}, metav1.CreateOptions{})
		require.NoError(t, err, "Error creating owner sheriff %s|default/owner", wsPath)

		t.Logf("Creating owned configmap")
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").
			Apply(ctx, corev1ac.ConfigMap("owned", "default").
				WithOwnerReferences(metav1ac.OwnerReference().
					WithAPIVersion(sheriffsGVR.GroupVersion().String()).
					WithKind(owner.GetKind()).
					WithName(owner.GetName()).
					WithUID(owner.GetUID())),
				metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
		require.NoError(t, err, "Error applying owned configmap %s|default/owned", wsPath)
	}

	t.Logf("Deleting all sheriffs")
	for _, ws := range workspaces {
		err = dynamicClusterClient.Cluster(ws).Resource(sheriffsGVR).Namespace("default").
			DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
		require.NoError(t, err, "Error deleting all sheriffs in %s", ws)
	}

	t.Logf("Waiting for the owned configmaps to be garbage collected")
	framework.Eventually(t, func() (bool, string) {
		_, err1 := kubeClusterClient.Cluster(ws1Path).CoreV1().ConfigMaps("default").Get(ctx, "owned", metav1.GetOptions{})
		_, err2 := kubeClusterClient.Cluster(ws2Path).CoreV1().ConfigMaps("default").Get(ctx, "owned", metav1.GetOptions{})
		return apierrors.IsNotFound(err1) && apierrors.IsNotFound(err2), "configmaps not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmaps to be garbage collected")
}

func TestGarbageCollectorVersionedCRDs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	group := framework.UniqueGroup(".io")

	sheriffCRD := apifixtures.NewSheriffsCRDWithVersions(group, "v1", "v2")

	wsPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-crd-versions"))

	t.Logf("Install a versioned sheriffs CRD into workspace %q", wsPath)
	bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffCRD)

	sheriffsGVRv1 := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	sheriffsGVRv2 := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v2"}

	t.Logf("Creating owner v1 sheriff")
	ownerv1, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owner-v1",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner sheriff %s|default/owner-v1", wsPath)

	t.Logf("Creating owned v1 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v1",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv1.GetAPIVersion(),
							"kind":       ownerv1.GetKind(),
							"name":       ownerv1.GetName(),
							"uid":        ownerv1.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v1", wsPath)

	t.Logf("Creating owned v2 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v2",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv1.GetAPIVersion(),
							"kind":       ownerv1.GetKind(),
							"name":       ownerv1.GetName(),
							"uid":        ownerv1.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v2", wsPath)

	t.Logf("Deleting owner v1 sheriff")
	err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Delete(ctx, ownerv1.GetName(), metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting sheriff %s in %s", ownerv1.GetName(), wsPath)

	t.Logf("Waiting for the owned sheriffs to be garbage collected")
	framework.Eventually(t, func() (bool, string) {
		_, err1 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").Get(ctx, "owned-v1", metav1.GetOptions{})
		_, err2 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").Get(ctx, "owned-v2", metav1.GetOptions{})
		return apierrors.IsNotFound(err1) && apierrors.IsNotFound(err2), "sheriffs not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned sheriffs to be garbage collected")

	t.Logf("Creating owner v2 sheriff")
	ownerv2, err := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owner-v2",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner sheriff %s|default/owner-v2", wsPath)

	t.Logf("Creating owned v1 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv1.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v1",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv2.GetAPIVersion(),
							"kind":       ownerv2.GetKind(),
							"name":       ownerv2.GetName(),
							"uid":        ownerv2.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v1", wsPath)

	t.Logf("Creating owned v2 sheriff")
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sheriffsGVRv2.GroupVersion().String(),
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": "owned-v2",
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion": ownerv2.GetAPIVersion(),
							"kind":       ownerv2.GetKind(),
							"name":       ownerv2.GetName(),
							"uid":        ownerv2.GetUID(),
						},
					},
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned sheriff %s|default/owned-v2", wsPath)

	t.Logf("Deleting owner v2 sheriff")
	err = dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").
		Delete(ctx, ownerv2.GetName(), metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting sheriff %s in %s", ownerv2.GetName(), wsPath)

	t.Logf("Waiting for the owned sheriffs to be garbage collected")
	framework.Eventually(t, func() (bool, string) {
		_, err1 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv1).Namespace("default").Get(ctx, "owned-v1", metav1.GetOptions{})
		_, err2 := dynamicClusterClient.Cluster(wsPath).Resource(sheriffsGVRv2).Namespace("default").Get(ctx, "owned-v2", metav1.GetOptions{})
		return apierrors.IsNotFound(err1) && apierrors.IsNotFound(err2), "sheriffs not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned sheriffs to be garbage collected")
}

func TestGarbageCollectorClusterScopedCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	group := framework.UniqueGroup(".io")

	crd := NewClusterScopedCRD(group, "clustered")

	wsPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("gc-crd-cluster-scope"))

	t.Logf("Install cluster-scoped CRD into workspace %q", wsPath)
	bootstrapCRD(t, wsPath, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions(), crd)

	gvr := schema.GroupVersionResource{Group: group, Resource: crd.Spec.Names.Plural, Version: "v1"}

	t.Logf("Creating owner clustered")
	owner, err := dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": gvr.GroupVersion().String(),
				"kind":       crd.Spec.Names.Kind,
				"metadata": map[string]interface{}{
					"name": "owner",
				},
			},
		}, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owner clustered %s|default/owner", wsPath)

	t.Logf("Creating owned clustered")
	owned := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": gvr.GroupVersion().String(),
			"kind":       crd.Spec.Names.Kind,
			"metadata": map[string]interface{}{
				"name": "owned",
			},
		},
	}
	owned.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: gvr.GroupVersion().String(),
			Kind:       owner.GetKind(),
			Name:       owner.GetName(),
			UID:        owner.GetUID(),
		},
	})
	_, err = dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Create(ctx, owned, metav1.CreateOptions{})
	require.NoError(t, err, "Error creating owned clustered %s|default/owned", wsPath)

	t.Logf("Deleting owner clustered")
	err = dynamicClusterClient.Cluster(wsPath).Resource(gvr).
		Delete(ctx, "owner", metav1.DeleteOptions{})
	require.NoError(t, err, "Error deleting owner clustered in %s", wsPath)

	t.Logf("Waiting for the owned clustered to be garbage collected")
	framework.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.Cluster(wsPath).Resource(gvr).
			Get(ctx, "owner", metav1.GetOptions{})
		return apierrors.IsNotFound(err), "owned clustered not garbage collected"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned clustered to be garbage collected")
}

func NewClusterScopedCRD(group, name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", pluralize(name), group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: name,
				Plural:   pluralize(name),
				Kind:     strings.ToTitle(name),
				ListKind: strings.ToTitle(name) + "List",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}
}

func pluralize(name string) string {
	switch string(name[len(name)-1]) {
	case "s":
		return name + "es"
	case "y":
		return strings.TrimSuffix(name, "y") + "ies"
	}

	return name + "s"
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Path,
	client kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	t.Helper()

	ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	t.Cleanup(cancelFunc)

	err := configcrds.CreateSingle(ctx, client.Cluster(clusterName), crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}
