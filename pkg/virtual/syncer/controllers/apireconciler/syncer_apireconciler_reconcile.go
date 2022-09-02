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

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
)

func (c *APIReconciler) reconcile(ctx context.Context, apiDomainKey dynamiccontext.APIDomainKey, syncTarget *workloadv1alpha1.SyncTarget) error {
	c.mutex.RLock()
	oldSet := c.apiSets[apiDomainKey]
	c.mutex.RUnlock()

	logger := klog.FromContext(ctx)

	// collect APIResourceSchemas by syncTarget.
	apiResourceSchemas, schemaIdentites, err := c.getAllAcceptedResourceSchemas(syncTarget)
	if err != nil {
		return err
	}

	// add built-in apiResourceSchema
	for _, apiResourceSchema := range syncerSchemas {
		shallow := *apiResourceSchema
		if shallow.Annotations == nil {
			shallow.Annotations = make(map[string]string)
		}
		shallow.Annotations[logicalcluster.AnnotationKey] = logicalcluster.From(syncTarget).String()
		apiResourceSchemas[schema.GroupResource{
			Group:    apiResourceSchema.Spec.Group,
			Resource: apiResourceSchema.Spec.Names.Plural,
		}] = &shallow
	}

	// reconcile APIs for APIResourceSchemas
	newSet := apidefinition.APIDefinitionSet{}
	newGVRs := []string{}
	preservedGVR := []string{}
	for gr, apiResourceSchema := range apiResourceSchemas {
		for _, version := range apiResourceSchema.Spec.Versions {
			if !version.Served {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    gr.Group,
				Version:  version.Name,
				Resource: gr.Resource,
			}

			oldDef, found := oldSet[gvr]
			if found {
				oldDef := oldDef.(apiResourceSchemaApiDefinition)
				if oldDef.UID != apiResourceSchema.UID {
					logging.WithObject(logger, apiResourceSchema).V(4).Info("APIResourceSchema UID has changed:", "oldUID", oldDef.UID, "newUID", apiResourceSchema.UID)
				}
				if oldDef.IdentityHash != schemaIdentites[gr] {
					logging.WithObject(logger, apiResourceSchema).V(4).Info("APIResourceSchema identity hash has changed", "oldIdentityHash", oldDef.IdentityHash, "newIdentityHash", schemaIdentites[gr])
				}
				if oldDef.UID == apiResourceSchema.UID && oldDef.IdentityHash == schemaIdentites[gr] {
					// this is the same schema and identity as before. no need to update.
					newSet[gvr] = oldDef
					preservedGVR = append(preservedGVR, gvrString(gvr))
					continue
				}
			}

			apiDefinition, err := c.createAPIDefinition(logicalcluster.From(syncTarget), syncTarget.Name, apiResourceSchema, version.Name, schemaIdentites[gr])
			if err != nil {
				logger.WithValues("gvr", gvr).Error(err, "failed to create API definition")
				continue
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: apiDefinition,
				UID:           apiResourceSchema.UID,
				IdentityHash:  schemaIdentites[gr],
			}
			newGVRs = append(newGVRs, gvrString(gvr))
		}
	}

	// cleanup old definitions
	removedGVRs := []string{}
	for gvr, oldDef := range oldSet {
		if _, found := newSet[gvr]; !found || oldDef != newSet[gvr] {
			removedGVRs = append(removedGVRs, gvrString(gvr))
			oldDef.TearDown()
		}
	}

	logging.WithObject(logger, syncTarget).WithValues("APIDomainKey", apiDomainKey).V(2).Info("Updating APIs for SyncTarget and APIDomainKey", "newGVRs", newGVRs, "preservedGVRs", preservedGVR, "removedGVRs", removedGVRs)

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

// getAllAcceptedResourceSchemas return all resourceSchemas from APIExports defined in this syncTarget filtered by the status.syncedResource
// of syncTarget such that only resources with accepted state is returned, together with their identityHash.
func (c *APIReconciler) getAllAcceptedResourceSchemas(syncTarget *workloadv1alpha1.SyncTarget) (map[schema.GroupResource]*apisv1alpha1.APIResourceSchema, map[schema.GroupResource]string, error) {
	apiExportKeys := getExportKeys(syncTarget)
	apiResourceSchemas := map[schema.GroupResource]*apisv1alpha1.APIResourceSchema{}

	identityHashByGroupResource := map[schema.GroupResource]string{}

	// get all identityHash for compatible APIs
	for _, syncedResource := range syncTarget.Status.SyncedResources {
		if syncedResource.State == workloadv1alpha1.ResourceSchemaAcceptedState {
			identityHashByGroupResource[schema.GroupResource{
				Group:    syncedResource.Group,
				Resource: syncedResource.Resource,
			}] = syncedResource.IdentityHash
		}
	}

	var errs []error
	for _, apiExportKey := range apiExportKeys {
		apiExport, err := c.apiExportLister.Get(apiExportKey)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}

		for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
			apiResourceSchema, err := c.apiResourceSchemaLister.Get(clusters.ToClusterAwareKey(logicalcluster.From(apiExport), schemaName))
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}

			gr := schema.GroupResource{
				Group:    apiResourceSchema.Spec.Group,
				Resource: apiResourceSchema.Spec.Names.Plural,
			}

			// if identityHash does not exist, it is not a compatible API.
			if _, ok := identityHashByGroupResource[gr]; ok {
				apiResourceSchemas[gr] = apiResourceSchema
			}
		}
	}

	return apiResourceSchemas, identityHashByGroupResource, errors.NewAggregate(errs)
}

// syncerSchemas contains a list of internal APIs that should be exposed for the syncer of any SyncTarget.
var syncerSchemas []*apisv1alpha1.APIResourceSchema

func init() {
	schemes := []*runtime.Scheme{legacyscheme.Scheme}
	openAPIDefinitionsGetters := []common.GetOpenAPIDefinitions{generatedopenapi.GetOpenAPIDefinitions}

	if apis, err := internalapis.CreateAPIResourceSchemas(schemes, openAPIDefinitionsGetters, syncerInternalAPIs...); err != nil {
		panic(err)
	} else {
		syncerSchemas = apis
	}
}

// syncerInternalAPIs provides a list of built-in APIs that are available for all workspaces accessed via the syncer virtual workspace.
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
