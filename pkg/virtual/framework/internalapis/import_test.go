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

package internalapis

import (
	"embed"
	"path"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kube-openapi/pkg/common"
	_ "k8s.io/kubernetes/pkg/apis/core/install"
	k8sopenapi "k8s.io/kubernetes/pkg/generated/openapi"
	"sigs.k8s.io/yaml"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
)

//go:embed fixtures/*.yaml
var embeddedResources embed.FS

func TestImportInternalAPIs(t *testing.T) {
	apisToImport := []InternalAPI{
		{
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "namespaces",
				Singular: "namespace",
				Kind:     "Namespace",
			},
			GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
			Instance:      &corev1.Namespace{},
			ResourceScope: apiextensionsv1.ClusterScoped,
			HasStatus:     true,
		},
		{
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "configmaps",
				Singular: "configmap",
				Kind:     "ConfigMap",
			},
			GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
			Instance:      &corev1.ConfigMap{},
			ResourceScope: apiextensionsv1.NamespaceScoped,
		},
		{
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "synctargets",
				Singular: "synctarget",
				Kind:     "SyncTarget",
			},
			GroupVersion:  schema.GroupVersion{Group: "workload.kcp.dev", Version: "v1alpha1"},
			Instance:      &workloadv1alpha1.SyncTarget{},
			ResourceScope: apiextensionsv1.ClusterScoped,
			HasStatus:     true,
		},
	}
	workloadScheme := runtime.NewScheme()
	err := workloadv1alpha1.AddToScheme(workloadScheme)
	require.NoError(t, err)
	schemas, err := CreateAPIResourceSchemas(
		[]*runtime.Scheme{clientgoscheme.Scheme, workloadScheme},
		[]common.GetOpenAPIDefinitions{k8sopenapi.GetOpenAPIDefinitions, kcpopenapi.GetOpenAPIDefinitions},
		apisToImport...)
	require.NoError(t, err)
	require.Len(t, schemas, 3)
	for _, schema := range schemas {
		expectedContent, err := embeddedResources.ReadFile(path.Join("fixtures", schema.Spec.Names.Plural+".yaml"))
		require.NoError(t, err)
		actualContent, err := yaml.Marshal(schema)
		require.NoError(t, err)
		require.Empty(t, cmp.Diff(strings.Split(string(expectedContent), "\n"), strings.Split(string(actualContent), "\n")))
	}
}
