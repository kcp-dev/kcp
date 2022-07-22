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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// NewSheriffsCRDWithSchemaDescription returns a minimal sheriffs CRD in the API group specified with the description
// used as the object's description in the OpenAPI schema.
func NewSheriffsCRDWithSchemaDescription(group, description string) *v1.CustomResourceDefinition {
	crdName := fmt.Sprintf("sheriffs.%s", group)

	crd := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: crdName,
		},
		Spec: v1.CustomResourceDefinitionSpec{
			Group: group,
			Names: v1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: "Namespaced",
			Versions: []v1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &v1.CustomResourceValidation{
						OpenAPIV3Schema: &v1.JSONSchemaProps{
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

// NewSheriffsAPIResourceSchemaWithDescription returns a new apisv1alpha1.APIResourceSchema for a sheriffs resource in
// the group specified, and with the description used as the object's description in the OpenAPI schema.
func NewSheriffsAPIResourceSchemaWithDescription(group, description string) *apisv1alpha1.APIResourceSchema {
	name := fmt.Sprintf("today.sheriffs.%s", group)

	ret := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: v1.CustomResourceDefinitionNames{
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
							&v1.JSONSchemaProps{
								Type:        "object",
								Description: description,
							},
						),
					},
				},
			},
		},
	}

	return ret
}

// NewSheriffsAPIExport returns a new apisv1alpha1.APIExport named apiExportName pointing at schemaName.
func NewSheriffsAPIExport(apiExportName, schemaName string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExportName,
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{schemaName},
		},
	}
}

// CreateSheriffsSchemaAndExport creates a sheriffs apisv1alpha1.APIResourceSchema and then creates a apisv1alpha1.APIExport
// to export it.
func CreateSheriffsSchemaAndExport(
	ctx context.Context,
	t *testing.T,
	clusterName logicalcluster.Name,
	clusterClient kcpclientset.ClusterInterface,
	group string,
	description string,
) {
	schema := NewSheriffsAPIResourceSchemaWithDescription(group, description)
	t.Logf("Creating APIResourceSchema %s|%s", clusterName, schema.Name)
	_, err := clusterClient.Cluster(clusterName).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIResourceSchema %s|%s", clusterName, schema.Name)

	export := NewSheriffsAPIExport(group, schema.Name)
	t.Logf("Creating APIExport %s|%s", clusterName, export.Name)
	_, err = clusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport %s|%s", clusterName, export.Name)
}

// CreateSheriff creates an instance of a Sheriff CustomResource in the logical cluster identified by clusterName, in
// the specific API group, and with the specified name.
func CreateSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient dynamic.ClusterInterface,
	clusterName logicalcluster.Name,
	group, name string,
) {
	name = strings.Replace(name, ":", "-", -1)

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
