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

package apifixtures

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// NewSheriffsCRDWithSchemaDescription returns a minimal sheriffs CRD in the API group specified with the description
// used as the object's description in the OpenAPI schema.
func NewSheriffsCRDWithSchemaDescription(group, description string) *apiextensionsv1.CustomResourceDefinition {
	crdName := fmt.Sprintf("sheriffs.%s", group)

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: "Namespaced",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:        "object",
							Description: description,
						},
					},
				},
			},
		},
	}

	return crd
}

func NewSheriffsCRDWithVersions(group string, versions ...string) *apiextensionsv1.CustomResourceDefinition {
	crdName := fmt.Sprintf("sheriffs.%s", group)

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: "Namespaced",
		},
	}

	for i, version := range versions {
		crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name:    version,
			Served:  true,
			Storage: i == len(versions)-1,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:        "object",
					Description: "sheriff " + version,
				},
			},
		})
	}

	return crd
}

// CreateSheriffsSchemaAndExport creates a sheriffs apisv1alpha1.APIResourceSchema and then creates a apisv1alpha1.APIExport to export it.
func CreateSheriffsSchemaAndExport(
	ctx context.Context,
	t *testing.T,
	clusterName logicalcluster.Name,
	clusterClient kcpclientset.ClusterInterface,
	group string,
	description string,
) {
	schema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("today.sheriffs.%s", group),
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: "Namespaced",
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: jsonOrDie(
							&apiextensionsv1.JSONSchemaProps{
								Type:        "object",
								Description: description,
							},
						),
					},
				},
			},
		},
	}

	t.Logf("Creating APIResourceSchema %s|%s", clusterName, schema.Name)
	_, err := clusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIResourceSchema %s|%s", clusterName, schema.Name)

	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: group,
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{schema.Name},
		},
	}

	t.Logf("Creating APIExport %s|%s", clusterName, export.Name)
	_, err = clusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport %s|%s", clusterName, export.Name)
}

// CreateSheriff creates an instance of a Sheriff CustomResource in the logical cluster identified by clusterName, in
// the specific API group, and with the specified name.
// Deprecated: use local fixtures instead.
func CreateSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Name,
	group, name string,
) {
	name = strings.ReplaceAll(name, ":", "-")

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	// CRDs are asynchronously served because they are informer based.
	framework.Eventually(t, func() (bool, string) {
		if _, err := dynamicClusterClient.Cluster(clusterName).Resource(sheriffsGVR).Namespace("default").Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": group + "/v1",
				"kind":       "Sheriff",
				"metadata": map[string]interface{}{
					"name": name,
				},
			},
		}, metav1.CreateOptions{}); err != nil {
			return false, fmt.Sprintf("failed to create Sheriff %s|%s: %v", clusterName, name, err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "error creating Sheriff %s|%s", clusterName, name)
}

func jsonOrDie(obj interface{}) []byte {
	ret, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return ret
}
