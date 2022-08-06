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

package apireconciler

import (
	"context"
	"fmt"
	"sort"

	"github.com/kcp-dev/logicalcluster/v2"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	flowcontrolv1beta1 "k8s.io/api/flowcontrol/v1beta1"
	flowcontrolv1beta2 "k8s.io/api/flowcontrol/v1beta2"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
)

func (c *APIReconciler) reconcile(ctx context.Context, apiExport *apisv1alpha1.APIExport, apiDomainKey dynamiccontext.APIDomainKey) error {
	if apiExport == nil || apiExport.Status.IdentityHash == "" {
		c.mutex.RLock()
		_, found := c.apiSets[apiDomainKey]
		c.mutex.RUnlock()

		if !found {
			klog.V(3).Infof("No APIs found for API domain key %s", apiDomainKey)
			return nil
		}

		// remove the APIDomain
		c.mutex.Lock()
		defer c.mutex.Unlock()
		klog.V(2).Infof("Deleting APIs for API domain key %s", apiDomainKey)
		delete(c.apiSets, apiDomainKey)
		return nil
	}

	c.mutex.RLock()
	oldSet := c.apiSets[apiDomainKey]
	c.mutex.RUnlock()

	// Get schemas and identities for base api export.
	apiResourceSchemas, err := c.getSchemasFromAPIExport(apiExport)
	if err != nil {
		return err
	}
	identities := map[schema.GroupResource]string{}
	for gr := range apiResourceSchemas {
		identities[gr] = apiExport.Status.IdentityHash
	}

	// Find schemas for claimed resources
	claims := map[schema.GroupResource]*apisv1alpha1.PermissionClaim{}
	for i := range apiExport.Spec.PermissionClaims {
		pc := &apiExport.Spec.PermissionClaims[i]

		// APIExport resource trump over claims.
		gr := schema.GroupResource{Group: pc.Group, Resource: pc.Resource}
		if _, found := apiResourceSchemas[gr]; found {
			if otherClaim, found := claims[gr]; found {
				klog.Warningf("Permission claim %v for APIExport %s|%s is shadowed by %v", pc, logicalcluster.From(apiExport), apiExport.Name, otherClaim)
			} else {
				klog.Warningf("Permission claim %v for APIExport %s|%s is shadowed exported resource", pc, logicalcluster.From(apiExport), apiExport.Name)
			}
			continue
		}

		// internal APIs have no identity and a fixed schema.
		internal, apiResourceSchema := isPermissionClaimForInternalAPI(pc)
		if internal {
			shallow := *apiResourceSchema
			if shallow.Annotations == nil {
				shallow.Annotations = make(map[string]string)
			}
			shallow.Annotations[logicalcluster.AnnotationKey] = logicalcluster.From(apiExport).String()
			apiResourceSchemas[gr] = &shallow
			continue
		} else if pc.IdentityHash == "" {
			// TODO: add validation through admission to avoid this case
			klog.Warningf("Permission claim %v for APIExport %s|%s is not internal and does not have an identity hash", pc, logicalcluster.From(apiExport), apiExport.Name)
			continue
		}

		exports, err := c.apiExportIndexer.ByIndex(indexers.IndexAPIExportByIdentity, pc.IdentityHash)
		if err != nil {
			return err
		}

		// there might be multiple exports with the same identity hash all exporting the same GR.
		// This is fine. Same identity means same owner. They have to ensure the schemas are compatible.
		// The kcp server resource handlers will make sure the right structural schemas are applied. Here,
		// we can just pick one. To make it deterministic, we sort the exports.
		sort.Slice(exports, func(i, j int) bool {
			a := exports[i].(*apisv1alpha1.APIExport)
			b := exports[j].(*apisv1alpha1.APIExport)
			return a.Name < b.Name && logicalcluster.From(a).String() < logicalcluster.From(b).String()
		})

		for _, obj := range exports {
			export := obj.(*apisv1alpha1.APIExport)
			candidates, err := c.getSchemasFromAPIExport(export)
			if err != nil {
				return err
			}
			for _, apiResourceSchema := range candidates {
				if apiResourceSchema.Spec.Group != pc.Group || apiResourceSchema.Spec.Names.Plural != pc.Resource {
					continue
				}
				apiResourceSchemas[gr] = apiResourceSchema
				identities[gr] = pc.IdentityHash
				claims[gr] = pc
			}
		}
	}

	// reconcile APIs for APIResourceSchemas
	newSet := apidefinition.APIDefinitionSet{}
	newGVRs := []string{}
	preservedGVR := []string{}
	for _, apiResourceSchema := range apiResourceSchemas {
		for _, version := range apiResourceSchema.Spec.Versions {
			if !version.Served {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    apiResourceSchema.Spec.Group,
				Version:  version.Name,
				Resource: apiResourceSchema.Spec.Names.Plural,
			}

			oldDef, found := oldSet[gvr]
			if found {
				oldDef := oldDef.(apiResourceSchemaApiDefinition)
				if oldDef.UID == apiResourceSchema.UID && oldDef.IdentityHash == apiExport.Status.IdentityHash {
					// this is the same schema and identity as before. no need to update.
					newSet[gvr] = oldDef
					preservedGVR = append(preservedGVR, gvrString(gvr))
					continue
				}
			}

			var labelReqs labels.Requirements
			if c := claims[gvr.GroupResource()]; c != nil {
				key, label, err := permissionclaims.ToLabelKeyAndValue(*c)
				if err != nil {
					return fmt.Errorf(fmt.Sprintf("failed to convert permission claim %v to label key and value: %v", c, err))
				}
				selector := labels.SelectorFromSet(labels.Set{key: label})
				var selectable bool
				labelReqs, selectable = selector.Requirements()
				if !selectable {
					return fmt.Errorf("permission claim %v for APIExport %s|%s is not selectable", c, logicalcluster.From(apiExport), apiExport.Name)
				}
			}

			apiDefinition, err := c.createAPIDefinition(apiResourceSchema, version.Name, identities[gvr.GroupResource()], labelReqs)
			if err != nil {
				// TODO(ncdc): would be nice to expose some sort of user-visible error
				klog.Errorf("error creating api definition for schema: %v/%v err: %v", apiResourceSchema.Spec.Group, apiResourceSchema.Spec.Names, err)
				continue
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: apiDefinition,
				UID:           apiResourceSchema.UID,
				IdentityHash:  apiExport.Status.IdentityHash,
			}
			newGVRs = append(newGVRs, gvrString(gvr))
		}
	}

	// cleanup old definitions
	removedGVRs := []string{}
	for gvr, oldDef := range oldSet {
		if newDef, found := newSet[gvr]; !found || oldDef.(apiResourceSchemaApiDefinition).APIDefinition != newDef.(apiResourceSchemaApiDefinition).APIDefinition {
			removedGVRs = append(removedGVRs, gvrString(gvr))
			oldDef.TearDown()
		}
	}

	klog.V(2).Infof("Updating APIs for APIExport %s|%s: new=%v, preserved=%v, removed=%v", logicalcluster.From(apiExport), apiExport.Name, newGVRs, preservedGVR, removedGVRs)

	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.apiSets[apiDomainKey] = newSet

	return nil
}

type apiResourceSchemaApiDefinition struct {
	apidefinition.APIDefinition

	UID          types.UID
	IdentityHash string
}

func gvrString(gvr schema.GroupVersionResource) string {
	group := gvr.Group
	if group == "" {
		group = "core"
	}
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Version, group)
}

func (c *APIReconciler) getSchemasFromAPIExport(apiExport *apisv1alpha1.APIExport) (map[schema.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	apiResourceSchemas := map[schema.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiResourceSchema, err := c.apiResourceSchemaLister.Get(clusters.ToClusterAwareKey(logicalcluster.From(apiExport), schemaName))
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if apierrors.IsNotFound(err) {
			klog.V(3).Infof("APIResourceSchema %s in APIExport %s|% not found", schemaName, apiExport.Namespace, apiExport.Name)
			continue
		}
		apiResourceSchemas[schema.GroupResource{Group: apiResourceSchema.Spec.Group, Resource: apiResourceSchema.Spec.Names.Plural}] = apiResourceSchema
	}

	return apiResourceSchemas, nil
}

func isPermissionClaimForInternalAPI(claim *apisv1alpha1.PermissionClaim) (bool, *apisv1alpha1.APIResourceSchema) {
	for _, schema := range internalAPIResourceSchemas {
		if claim.GroupResource.Group == schema.Spec.Group && claim.GroupResource.Resource == schema.Spec.Names.Plural {
			return true, schema
		}
	}
	return false, nil
}

// Create APIResourceSchemas for built-in APIs available as permission claims for APIExport virtual workspace.
func init() {
	schemes := []*runtime.Scheme{genericcontrolplanescheme.Scheme}
	openAPIDefinitionsGetters := []common.GetOpenAPIDefinitions{generatedopenapi.GetOpenAPIDefinitions}

	if apis, err := internalapis.CreateAPIResourceSchemas(schemes, openAPIDefinitionsGetters, InternalAPIs...); err != nil {
		panic(err)
	} else {
		internalAPIResourceSchemas = apis
	}
}

// internalAPIResourceSchemas contains a list of APIResourceSchema for built-in APIs that are
// available to be permission-claimed for APIExport virtual workspace
var internalAPIResourceSchemas []*apisv1alpha1.APIResourceSchema

var InternalAPIs = []internalapis.InternalAPI{
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
			Plural:   "limitranges",
			Singular: "limitrange",
			Kind:     "LimitRange",
		},
		GroupVersion:  schema.GroupVersion{Group: "", Version: "v1"},
		Instance:      &corev1.LimitRange{},
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
			Plural:   "tokenreviews",
			Singular: "tokenreview",
			Kind:     "TokenReview",
		},
		GroupVersion:  schema.GroupVersion{Group: "authentication.k8s.io", Version: "v1"},
		Instance:      &authenticationv1.TokenReview{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "localsubjectaccessreviews",
			Singular: "localsubjectaccessreview",
			Kind:     "LocalSubjectAccessReview",
		},
		GroupVersion:  schema.GroupVersion{Group: "authorization.k8s.io", Version: "v1"},
		Instance:      &authorizationv1.LocalSubjectAccessReview{},
		ResourceScope: apiextensionsv1.NamespaceScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "selfsubjectaccessreviews",
			Singular: "selfsubjectaccessreview",
			Kind:     "SelfSubjectAccessReview",
		},
		GroupVersion:  schema.GroupVersion{Group: "authorization.k8s.io", Version: "v1"},
		Instance:      &authorizationv1.SelfSubjectAccessReview{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "selfsubjectrulesreviews",
			Singular: "selfsubjectrulesreview",
			Kind:     "SelfSubjectRulesReview",
		},
		GroupVersion:  schema.GroupVersion{Group: "authorization.k8s.io", Version: "v1"},
		Instance:      &authorizationv1.SelfSubjectRulesReview{},
		ResourceScope: apiextensionsv1.ClusterScoped,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "subjectaccessreviews",
			Singular: "subjectaccessreview",
			Kind:     "SubjectAccessReview",
		},
		GroupVersion:  schema.GroupVersion{Group: "authorization.k8s.io", Version: "v1"},
		Instance:      &authorizationv1.SubjectAccessReview{},
		ResourceScope: apiextensionsv1.ClusterScoped,
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
			Plural:   "flowschemas",
			Singular: "flowschema",
			Kind:     "FlowSchema",
		},
		GroupVersion:  schema.GroupVersion{Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta1"},
		Instance:      &flowcontrolv1beta1.FlowSchema{},
		ResourceScope: apiextensionsv1.ClusterScoped,
		HasStatus:     true,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "prioritylevelconfigurations",
			Singular: "prioritylevelconfiguration",
			Kind:     "PriorityLevelConfiguration",
		},
		GroupVersion:  schema.GroupVersion{Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta1"},
		Instance:      &flowcontrolv1beta1.PriorityLevelConfiguration{},
		ResourceScope: apiextensionsv1.ClusterScoped,
		HasStatus:     true,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "flowschemas",
			Singular: "flowschema",
			Kind:     "FlowSchema",
		},
		GroupVersion:  schema.GroupVersion{Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta2"},
		Instance:      &flowcontrolv1beta2.FlowSchema{},
		ResourceScope: apiextensionsv1.ClusterScoped,
		HasStatus:     true,
	},
	{
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "prioritylevelconfigurations",
			Singular: "prioritylevelconfiguration",
			Kind:     "PriorityLevelConfiguration",
		},
		GroupVersion:  schema.GroupVersion{Group: "flowcontrol.apiserver.k8s.io", Version: "v1beta2"},
		Instance:      &flowcontrolv1beta2.PriorityLevelConfiguration{},
		ResourceScope: apiextensionsv1.ClusterScoped,
		HasStatus:     true,
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
}
