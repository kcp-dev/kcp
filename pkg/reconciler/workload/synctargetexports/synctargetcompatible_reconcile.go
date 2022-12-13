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

package synctargetexports

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/schemacompat"
)

// apiCompatibleReconciler sets state for each synced resource based on resource schema and apiimports.
// TODO(qiujian06) this should be done in syncer when resource schema(or crd) is exposed by syncer virtual workspace.
type apiCompatibleReconciler struct {
	getAPIExport           func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getResourceSchema      func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	listAPIResourceImports func(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.APIResourceImport, error)
}

func (e *apiCompatibleReconciler) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (*workloadv1alpha1.SyncTarget, error) {
	var errs []error
	schemaMap := map[schema.GroupVersionResource]*apiextensionsv1.JSONSchemaProps{}

	// Get json schema from all related resource schemas
	for _, exportRef := range syncTarget.Spec.SupportedAPIExports {
		if exportRef.Path == "" {
			exportRef.Path = logicalcluster.From(syncTarget).String()
		}
		export, err := e.getAPIExport(logicalcluster.NewPath(exportRef.Path), exportRef.Export)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}

		for _, schemaName := range export.Spec.LatestResourceSchemas {
			resourceSchema, err := e.getResourceSchema(logicalcluster.From(export), schemaName)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
			}

			for _, v := range resourceSchema.Spec.Versions {
				jsonSchema, err := v.GetSchema()
				if err != nil {
					errs = append(errs, err)
					continue
				}
				schemaMap[schema.GroupVersionResource{
					Group:    resourceSchema.Spec.Group,
					Resource: resourceSchema.Spec.Names.Plural,
					Version:  v.Name,
				}] = jsonSchema
			}
		}
	}

	lcluster := logicalcluster.From(syncTarget)
	apiImportMap := map[schema.GroupVersionResource]*apiextensionsv1.JSONSchemaProps{}
	apiImports, err := e.listAPIResourceImports(lcluster)
	if err != nil {
		return syncTarget, err
	}

	for _, apiImport := range apiImports {
		jsonSchema, err := apiImport.Spec.GetSchema()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		apiImportMap[schema.GroupVersionResource{
			Group:    apiImport.Spec.GroupVersion.Group,
			Version:  apiImport.Spec.GroupVersion.Version,
			Resource: apiImport.Spec.Plural,
		}] = jsonSchema
	}

	for i, syncedRsesource := range syncTarget.Status.SyncedResources {
		for _, v := range syncedRsesource.Versions {
			gvr := schema.GroupVersionResource{Group: syncedRsesource.Group, Resource: syncedRsesource.Resource, Version: v}
			upstreamSchema, ok := schemaMap[gvr]
			if !ok {
				syncTarget.Status.SyncedResources[i].State = workloadv1alpha1.ResourceSchemaPendingState
				continue
			}

			downStreamSchema, ok := apiImportMap[gvr]
			if !ok {
				syncTarget.Status.SyncedResources[i].State = workloadv1alpha1.ResourceSchemaIncompatibleState
				continue
			}

			_, err := schemacompat.EnsureStructuralSchemaCompatibility(
				field.NewPath(gvr.String()), upstreamSchema, downStreamSchema, false)
			if err != nil {
				syncTarget.Status.SyncedResources[i].State = workloadv1alpha1.ResourceSchemaIncompatibleState
				continue
			}

			// since version is ordered, so if the current version is comptaible, we can skip the check on other versions.
			syncTarget.Status.SyncedResources[i].State = workloadv1alpha1.ResourceSchemaAcceptedState
			break
		}
	}

	return syncTarget, errors.NewAggregate(errs)
}
