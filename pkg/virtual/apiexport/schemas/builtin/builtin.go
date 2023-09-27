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
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	admissionregistrationv1alpha1 "k8s.io/api/admissionregistration/v1alpha1"
	certificatesv1 "k8s.io/api/certificates/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/common"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	kcpscheme "github.com/kcp-dev/kcp/pkg/server/scheme"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// Create APIResourceSchemas for built-in APIs available as permission claims
// for APIExport virtual workspace.
// TODO(hasheddan): this could be handled via code generation.
func init() {
	schemes := []*runtime.Scheme{kcpscheme.Scheme}
	openAPIDefinitionsGetters := []common.GetOpenAPIDefinitions{generatedopenapi.GetOpenAPIDefinitions}

	apis, err := internalapis.CreateAPIResourceSchemas(schemes, openAPIDefinitionsGetters, BuiltInAPIs...)
	if err != nil {
		panic(err)
	}
	builtInAPIResourceSchemas = make(map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema, len(apis))
	for _, api := range apis {
		builtInAPIResourceSchemas[apisv1alpha1.GroupResource{
			Group:    api.Spec.Group,
			Resource: api.Spec.Names.Plural,
		}] = api
	}
}

// IsBuiltInAPI indicates whether the API identified by group and resource is
// built-in.
func IsBuiltInAPI(gr apisv1alpha1.GroupResource) bool {
	_, exists := builtInAPIResourceSchemas[gr]
	return exists
}

// GetBuiltInAPISchema retrieves the APIResourceSchema for a built-in API.
func GetBuiltInAPISchema(gr apisv1alpha1.GroupResource) (*apisv1alpha1.APIResourceSchema, error) {
	schema, exists := builtInAPIResourceSchemas[gr]
	if !exists {
		return nil, fmt.Errorf("no schema found for built-in API %s.%s", gr.Resource, gr.Group)
	}
	return schema, nil
}

// builtInAPIResourceSchemas contains a list of APIResourceSchema for built-in
// APIs that are available to be permission-claimed for APIExport virtual
// workspace
var builtInAPIResourceSchemas map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema

// TODO(hasheddan): ideally this would not be public, but it is currently used
// in e2e tests. Consider refactoring to only allow immutable access.
var BuiltInAPIs = []internalapis.InternalAPI{
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
			Plural:   "events",
			Singular: "event",
			Kind:     "Event",
		},
		GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
		Instance:      &corev1.Event{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "resourcequotas",
			Singular: "resourcequota",
			Kind:     "ResourceQuota",
		},
		GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
		Instance:      &corev1.ResourceQuota{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
		HasStatus:     true,
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
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "clusterroles",
			Singular: "clusterrole",
			Kind:     "ClusterRole",
		},
		GroupVersion:  schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"},
		Instance:      &rbacv1.ClusterRole{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "clusterrolebindings",
			Singular: "clusterrolebinding",
			Kind:     "ClusterRoleBinding",
		},
		GroupVersion:  schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"},
		Instance:      &rbacv1.ClusterRoleBinding{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "roles",
			Singular: "role",
			Kind:     "Role",
		},
		GroupVersion:  schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"},
		Instance:      &rbacv1.Role{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "rolebindings",
			Singular: "rolebinding",
			Kind:     "RoleBinding",
		},
		GroupVersion:  schema.GroupVersion{Group: "rbac.authorization.k8s.io", Version: "v1"},
		Instance:      &rbacv1.RoleBinding{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "certificatesigningrequests",
			Singular: "certificatesigningrequest",
			Kind:     "CertificateSigningRequest",
		},
		GroupVersion:  schema.GroupVersion{Group: "certificates.k8s.io", Version: "v1"},
		Instance:      &certificatesv1.CertificateSigningRequest{},
		ResourceScope: apiextensionsv1.ClusterScoped,
		HasStatus:     true,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "leases",
			Singular: "lease",
			Kind:     "Lease",
		},
		GroupVersion:  schema.GroupVersion{Group: "coordination.k8s.io", Version: "v1"},
		Instance:      &coordinationv1.Lease{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "mutatingwebhookconfigurations",
			Singular: "mutatingwebhookconfiguration",
			Kind:     "MutatingWebhookConfiguration",
		},
		GroupVersion:  schema.GroupVersion{Group: "admissionregistration.k8s.io", Version: "v1"},
		Instance:      &admissionregistrationv1.MutatingWebhookConfiguration{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "validatingwebhookconfigurations",
			Singular: "validatingwebhookconfiguration",
			Kind:     "ValidatingWebhookConfiguration",
		},
		GroupVersion:  schema.GroupVersion{Group: "admissionregistration.k8s.io", Version: "v1"},
		Instance:      &admissionregistrationv1.ValidatingWebhookConfiguration{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "events",
			Singular: "event",
			Kind:     "Event",
		},
		GroupVersion:  schema.GroupVersion{Group: "events.k8s.io", Version: "v1"},
		Instance:      &eventsv1.Event{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "validatingadmissionpolicies",
			Singular: "validatingadmissionpolicy",
			Kind:     "ValidatingAdmissionPolicy",
		},
		GroupVersion:  schema.GroupVersion{Group: "admissionregistration.k8s.io", Version: "v1alpha1"},
		Instance:      &admissionregistrationv1alpha1.ValidatingAdmissionPolicy{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "validatingadmissionpolicybindings",
			Singular: "validatingadmissionpolicybinding",
			Kind:     "ValidatingAdmissionPolicyBinding",
		},
		GroupVersion:  schema.GroupVersion{Group: "admissionregistration.k8s.io", Version: "v1alpha1"},
		Instance:      &admissionregistrationv1alpha1.ValidatingAdmissionPolicyBinding{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
}
