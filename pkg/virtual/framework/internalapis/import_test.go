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
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kube-openapi/pkg/common"
	k8sopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"

	_ "k8s.io/kubernetes/pkg/apis/core/install"
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
				Plural:   "workspaces",
				Singular: "workspace",
				Kind:     "Workspace",
			},
			GroupVersion:  schema.GroupVersion{Group: "tenancy.kcp.io", Version: "v1alpha1"},
			Instance:      &tenancyv1alpha1.Workspace{},
			ResourceScope: apiextensionsv1.ClusterScoped,
			HasStatus:     true,
		},
	}
	tenancyScheme := runtime.NewScheme()
	err := tenancyv1alpha1.AddToScheme(tenancyScheme)
	require.NoError(t, err)
	schemas, err := CreateAPIResourceSchemas(
		[]*runtime.Scheme{clientgoscheme.Scheme, tenancyScheme},
		[]common.GetOpenAPIDefinitions{k8sopenapi.GetOpenAPIDefinitions, kcpopenapi.GetOpenAPIDefinitions},
		apisToImport...)
	require.NoError(t, err)
	require.Len(t, schemas, 3)
	for _, s := range schemas {
		expectedContent, err := embeddedResources.ReadFile(path.Join("fixtures", s.Spec.Names.Plural+".yaml"))
		require.NoError(t, err)
		actualContent, err := yaml.Marshal(s)
		require.NoError(t, err)
		// If you just changed the schema and wondering "how do I make this test pass?", uncomment the following line
		// os.WriteFile(path.Join("fixtures", s.Spec.Names.Plural+".yaml"), actualContent, 0644)
		require.Emptyf(t, cmp.Diff(strings.Split(string(expectedContent), "\n"), strings.Split(string(actualContent), "\n")), "%s was not identical to the expected content", s.Name)
	}
}
