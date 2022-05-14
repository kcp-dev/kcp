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

package apidefinition

import (
	"embed"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	common "k8s.io/kube-openapi/pkg/common"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	_ "k8s.io/kubernetes/pkg/apis/core/install"
	k8sopenapi "k8s.io/kubernetes/pkg/generated/openapi"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
)

//go:embed fixtures/*.yaml
var embeddedResources embed.FS

func TestImportInternalAPIs(t *testing.T) {
	apisToImport := []InternalAPI{
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "namespaces",
				Singular: "namespace",
				Kind:     "Namespace",
			},
			gv:        schema.GroupVersion{Group: "", Version: "v1"},
			instance:  &corev1.Namespace{},
			scope:     apiextensionsv1.ClusterScoped,
			hasStatus: true,
		},
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "configmaps",
				Singular: "configmap",
				Kind:     "ConfigMap",
			},
			gv:       schema.GroupVersion{Group: "", Version: "v1"},
			instance: &corev1.ConfigMap{},
			scope:    apiextensionsv1.NamespaceScoped,
		},
		{
			names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "workloadclusters",
				Singular: "workloadcluster",
				Kind:     "WorkloadCluster",
			},
			gv:        schema.GroupVersion{Group: "workload.kcp.dev", Version: "v1alpha1"},
			instance:  &v1alpha1.WorkloadCluster{},
			scope:     apiextensionsv1.ClusterScoped,
			hasStatus: true,
		},
	}
	tenancyScheme := runtime.NewScheme()
	err := v1alpha1.AddToScheme(tenancyScheme)
	require.NoError(t, err)
	importedAPISpecs, err := ImportInternalAPIs(
		[]*runtime.Scheme{legacyscheme.Scheme, tenancyScheme},
		[]common.GetOpenAPIDefinitions{k8sopenapi.GetOpenAPIDefinitions, kcpopenapi.GetOpenAPIDefinitions},
		apisToImport...)
	require.NoError(t, err)
	require.Len(t, importedAPISpecs, 3)
	for _, importedAPISpec := range importedAPISpecs {
		expectedContent, err := embeddedResources.ReadFile(path.Join("fixtures", importedAPISpec.Plural+".yaml"))
		require.NoError(t, err)
		actualContent, err := yaml.Marshal(importedAPISpec)
		require.NoError(t, err)
		assert.Equal(t, string(expectedContent), string(actualContent))
	}
}
