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

package conformance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	"github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPartialMetadataCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	workspacePath, _ := framework.NewOrganizationFixture(t, server)
	workspaceCRDClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating crd cluster client")

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "theforces.apps.wars.cloud",
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "apps.wars.cloud",
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "TheForce",
				ListKind: "TheForceList",
				Singular: "theforce",
				Plural:   "theforces",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Storage: true,
					Served:  true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Description: "is strong with this one",
							Type:        "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"enabled": {
											Type: "boolean",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	resource := schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  crd.Spec.Versions[0].Name,
		Resource: crd.Spec.Names.Plural,
	}

	{
		t.Log("Creating a new crd")
		out, err := workspaceCRDClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspacePath).Create(ctx, crd, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}

		t.Log("Validating that the crd was created correctly")
		require.Equal(t, crd.Name, out.Name)
		require.Equal(t, crd.Spec.Group, out.Spec.Group)
		require.Equal(t, crd.Spec.Versions[0].Name, out.Spec.Versions[0].Name)
		require.Equal(t, crd.Spec.Versions[0], out.Spec.Versions[0])
	}

	{
		t.Log("List resources with partial object metadata")
		metadataClient, err := metadata.NewForConfig(cfg)
		require.NoError(t, err)
		framework.Eventually(t, func() (success bool, reason string) {
			_, err = metadataClient.Cluster(workspacePath).Resource(resource).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	{
		t.Log("Creating a new object with the dynamic client")
		dynamicClient, err := dynamic.NewForConfig(cfg)
		require.NoError(t, err)
		out, err := dynamicClient.Cluster(workspacePath).Resource(resource).Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps.wars.cloud/v1",
				"kind":       "TheForce",
				"metadata": map[string]interface{}{
					"name": "test",
				},
				"spec": map[string]interface{}{
					"enabled": true,
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Log("Verifying that the spec is present")
		require.NotNil(t, out.Object["spec"])
		enabled, ok, err := unstructured.NestedBool(out.Object, "spec", "enabled")
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, enabled)
	}
}

func TestPartialMetadataSameCRDMultipleWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	// Create ws1. Using the root shard because both ws1 and ws2 must be on the same shard to exercise this issue.
	workspace1Path, workspace1 := framework.NewOrganizationFixture(t, server, framework.WithRootShard())
	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating workspace1 CRD client")

	group := framework.UniqueGroup(".io")
	t.Logf("Using API group %q", group)

	// Install CRD with only v1
	crdForGroupVersion := func(group, version string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "theforces." + group,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: group,
				Scope: apiextensionsv1.ClusterScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Kind:     "TheForce",
					ListKind: "TheForceList",
					Singular: "theforce",
					Plural:   "theforces",
				},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    version,
						Storage: true,
						Served:  true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Description: "is strong with this one",
								Type:        "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"enabled": {
												Type: "boolean",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}
	}

	crd := crdForGroupVersion(group, "v1")
	crdName := crd.Name
	t.Logf("Workspace %s: creating CRD with version %s", workspace1.Name, crd.Spec.Versions[0].Name)
	_, err = crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspace1Path).Create(ctx, crd, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Workspace %s: waiting for ready CRD with version %s", workspace1.Name, crd.Spec.Versions[0].Name)
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		crd, err := crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspace1Path).Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &crdConditionsAdapter{CustomResourceDefinition: crd}, nil
	}, framework.Is(conditionsv1alpha1.ConditionType(apiextensionsv1.Established)), wait.ForeverTestTimeout, 100*time.Millisecond)

	// Create ws2. Using the root shard because both ws1 and ws2 must be on the same shard to exercise this issue.
	workspace2Path, workspace2 := framework.NewOrganizationFixture(t, server, framework.WithRootShard())
	require.NoError(t, err)

	// Install CRD with only v2
	crd = crdForGroupVersion(group, "v2")
	t.Logf("Workspace %s: creating CRD with version %s", workspace2.Name, crd.Spec.Versions[0].Name)
	_, err = crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspace2Path).Create(ctx, crd, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Workspace %s: waiting for ready CRD with version %s", workspace2.Name, crd.Spec.Versions[0].Name)
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		crd, err := crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspace2Path).Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &crdConditionsAdapter{CustomResourceDefinition: crd}, nil
	}, framework.Is(conditionsv1alpha1.ConditionType(apiextensionsv1.Established)), wait.ForeverTestTimeout, 100*time.Millisecond)

	dynamicClusterClient, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)

	gvrV1 := schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  "v1",
		Resource: crd.Spec.Names.Plural,
	}

	crForGroupVersion := func(group, version string) *unstructured.Unstructured {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": fmt.Sprintf("%s/%s", group, version),
				"kind":       "TheForce",
				"metadata": map[string]interface{}{
					"name": "test",
				},
				"spec": map[string]interface{}{
					"enabled": true,
				},
			},
		}
	}

	// Create a CR in ws1
	t.Logf("Workspace %s: creating CR instance with version v1", workspace1.Name)
	cr1, err := dynamicClusterClient.Cluster(workspace1Path).Resource(gvrV1).Create(ctx, crForGroupVersion(group, "v1"), metav1.CreateOptions{})
	require.NoError(t, err)

	gvrV2 := schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  "v2",
		Resource: crd.Spec.Names.Plural,
	}

	// Create a CR in ws2
	t.Logf("Workspace %s: creating CR instance with version v2", workspace2.Name)
	cr2, err := dynamicClusterClient.Cluster(workspace2Path).Resource(gvrV2).Create(ctx, crForGroupVersion(group, "v2"), metav1.CreateOptions{})
	require.NoError(t, err)

	// Make sure we can list in ws1
	t.Logf("Workspace %s: listing CR instances", workspace1.Name)
	list, err := dynamicClusterClient.Cluster(workspace1Path).Resource(gvrV1).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, gvrV1.GroupVersion().String(), list.Items[0].GetAPIVersion())

	// Make sure we can list in ws2
	t.Logf("Workspace %s: listing CR instances", workspace2.Name)
	list, err = dynamicClusterClient.Cluster(workspace2Path).Resource(gvrV2).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, gvrV2.GroupVersion().String(), list.Items[0].GetAPIVersion())

	// Do a partial metadata list for v1
	wildcardCfg := server.RootShardSystemMasterBaseConfig(t)
	metadataClusterClient, err := metadata.NewForConfig(wildcardCfg)
	require.NoError(t, err)

	t.Logf("Listing partial metadata for version v1")
	framework.Eventually(t, func() (success bool, reason string) {
		metadataList, err := metadataClusterClient.Cluster(logicalcluster.Wildcard).Resource(gvrV1).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(metadataList.Items) != 2 {
			return false, fmt.Sprintf("expected 2 items, got %d: %#v", len(metadataList.Items), metadataList.Items)
		}
		expectedUIDs := sets.New[string](string(cr1.GetUID()), string(cr2.GetUID()))
		actualUIDs := sets.New[string]()
		for _, item := range metadataList.Items {
			actualUIDs.Insert(string(item.UID))
		}
		if diff := cmp.Diff(sets.List[string](expectedUIDs), sets.List[string](actualUIDs)); diff != "" {
			return false, fmt.Sprintf("didn't get expected UIDs: diff: %s", diff)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	// Do a partial metadata list for v2
	t.Logf("Listing partial metadata for version v2")
	framework.Eventually(t, func() (success bool, reason string) {
		metadataList, err := metadataClusterClient.Cluster(logicalcluster.Wildcard).Resource(gvrV2).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(metadataList.Items) != 2 {
			return false, fmt.Sprintf("expected 2 items, got %d: %#v", len(metadataList.Items), metadataList.Items)
		}
		expectedUIDs := sets.New[string](string(cr1.GetUID()), string(cr2.GetUID()))
		actualUIDs := sets.New[string]()
		for _, item := range metadataList.Items {
			actualUIDs.Insert(string(item.UID))
		}
		if diff := cmp.Diff(sets.List[string](expectedUIDs), sets.List[string](actualUIDs)); diff != "" {
			return false, fmt.Sprintf("didn't get expected UIDs: diff: %s", diff)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

type crdConditionsAdapter struct {
	*apiextensionsv1.CustomResourceDefinition
}

func (ca *crdConditionsAdapter) GetConditions() conditionsv1alpha1.Conditions {
	conditions := conditionsv1alpha1.Conditions{}
	for _, c := range ca.Status.Conditions {
		conditions = append(conditions, conditionsv1alpha1.Condition{
			Type:   conditionsv1alpha1.ConditionType(c.Type),
			Status: corev1.ConditionStatus(c.Status),
			// Default to None because NamespaceCondition lacks a Severity field
			Severity:           conditionsv1alpha1.ConditionSeverityNone,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}
	return conditions
}

func (ca *crdConditionsAdapter) SetConditions(conditions conditionsv1alpha1.Conditions) {
	outConditions := make([]apiextensionsv1.CustomResourceDefinitionCondition, 0, len(conditions))
	for _, c := range conditions {
		outConditions = append(outConditions, apiextensionsv1.CustomResourceDefinitionCondition{
			Type:   apiextensionsv1.CustomResourceDefinitionConditionType(c.Type),
			Status: apiextensionsv1.ConditionStatus(c.Status),
			// Severity is ignored
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}
	ca.Status.Conditions = outConditions
}
