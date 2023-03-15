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

package builtin

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/apis/core/install/genericcontrolplane"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// syncerSchemas contains a list of internal APIs that should be exposed for the
// syncer of any SyncTarget.
var SyncerSchemas map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema

func init() {
	genericcontrolplane.Install(legacyscheme.Scheme)
	schemes := []*runtime.Scheme{legacyscheme.Scheme}
	openAPIDefinitionsGetters := []common.GetOpenAPIDefinitions{generatedopenapi.GetOpenAPIDefinitions}

	apis, err := internalapis.CreateAPIResourceSchemas(schemes, openAPIDefinitionsGetters, syncerInternalAPIs...)
	if err != nil {
		panic(err)
	}

	SyncerSchemas = make(map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema, len(apis))
	for _, api := range apis {
		SyncerSchemas[apisv1alpha1.GroupResource{
			Group:    api.Spec.Group,
			Resource: api.Spec.Names.Plural,
		}] = api
	}
}

// syncerInternalAPIs provides a list of built-in APIs that are available for
// all workspaces accessed via the syncer virtual workspace.
var syncerInternalAPIs = []internalapis.InternalAPI{
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
			Plural:   "secrets",
			Singular: "secret",
			Kind:     "Secret",
		},
		GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
		Instance:      &corev1.Secret{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "serviceaccounts",
			Singular: "serviceaccount",
			Kind:     "ServiceAccount",
		},
		GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
		Instance:      &corev1.ServiceAccount{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
}
