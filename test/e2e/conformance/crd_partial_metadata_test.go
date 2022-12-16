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
	"testing"
	"time"

	"github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/client-go/metadata"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPartialMetadataCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	workspaceClusterName := framework.NewOrganizationFixture(t, server)
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
		out, err := workspaceCRDClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(workspaceClusterName.Path()).Create(ctx, crd, metav1.CreateOptions{})
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
			_, err = metadataClient.Cluster(workspaceClusterName.Path()).Resource(resource).List(ctx, metav1.ListOptions{})
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
		out, err := dynamicClient.Cluster(workspaceClusterName.Path()).Resource(resource).Create(ctx, &unstructured.Unstructured{
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
