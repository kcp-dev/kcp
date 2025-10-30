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

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
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

// CreateSheriffsSchemaAndExport creates a sheriffs apisv1alpha1.APIResourceSchema and then creates a apisv1alpha2.APIExport to export it.
func CreateSheriffsSchemaAndExport(
	ctx context.Context,
	t *testing.T,
	path logicalcluster.Path,
	clusterClient kcpclientset.ClusterInterface,
	group string,
	description string,
) {
	t.Helper()

	s := &apisv1alpha1.APIResourceSchema{
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

	t.Logf("Creating APIResourceSchema %s|%s", path, s.Name)
	_, err := clusterClient.Cluster(path).ApisV1alpha1().APIResourceSchemas().Create(ctx, s, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIResourceSchema %s|%s", path, s.Name)

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: group,
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "sheriffs",
					Group:  group,
					Schema: s.Name,
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}

	t.Logf("Creating APIExport %s|%s", path, export.Name)
	_, err = clusterClient.Cluster(path).ApisV1alpha2().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport %s|%s", path, export.Name)
}

// CreateSheriff creates an instance of a Sheriff CustomResource in the logical cluster identified by clusterName, in
// the specific API group, and with the specified name.
//
// Deprecated: use local fixtures instead.
func CreateSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Path,
	group, name string,
) {
	t.Helper()

	name = strings.ReplaceAll(name, ":", "-")

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	// CRDs are asynchronously served because they are informer based.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
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
