/*
Copyright 2026 The kcp Authors.

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
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

func findResourceSchemaByCachedResourceEndpointSlice(
	export *apisv1alpha2.APIExport,
	endpointSlice *cachev1alpha1.CachedResourceEndpointSlice,
) *apisv1alpha2.ResourceSchema {
	resIdx := slices.IndexFunc(export.Spec.Resources, func(res apisv1alpha2.ResourceSchema) bool {
		return res.Storage.Virtual != nil &&
			ptr.Deref(res.Storage.Virtual.Reference.APIGroup, "") == cachev1alpha1.SchemeGroupVersion.Group &&
			res.Storage.Virtual.Reference.Kind == "CachedResourceEndpointSlice" &&
			res.Storage.Virtual.Reference.Name == endpointSlice.Name
	})
	if resIdx >= 0 {
		return &export.Spec.Resources[resIdx]
	}
	return nil
}

func (c *APIReconciler) reconcile(ctx context.Context, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice, apiDomainKey dynamiccontext.APIDomainKey) error {
	logger := klog.FromContext(ctx)

	if endpointSlice == nil {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		delete(c.apiSets, apiDomainKey)
		return nil
	}

	if !conditions.IsTrue(endpointSlice, cachev1alpha1.CachedResourceValid) ||
		!conditions.IsTrue(endpointSlice, cachev1alpha1.APIExportValid) {
		logger.V(2).Info("CachedResourceEndpointSlice not ready")
		return nil
	}

	// Extract the CachedResource and APIExport referenced by the endpoint slice.

	cachedResourcePath := logicalcluster.NewPath(endpointSlice.Spec.CachedResource.Path)
	if cachedResourcePath.Empty() {
		cachedResourcePath = logicalcluster.From(endpointSlice).Path()
	}
	cachedResource, err := c.getCachedResourceByPath(cachedResourcePath, endpointSlice.Spec.CachedResource.Name)
	if err != nil {
		logger.Error(err, "failed to get CachedResource for CachedResourceEndpointSlice")
		return err
	}

	exportPath := logicalcluster.NewPath(endpointSlice.Spec.APIExport.Path)
	if exportPath.Empty() {
		exportPath = logicalcluster.From(endpointSlice).Path()
	}
	export, err := c.getAPIExportByPath(exportPath, endpointSlice.Spec.APIExport.Name)
	if err != nil {
		logger.Error(err, "failed to get APIExport for CachedResourceEndpointSlice")
		return err
	}

	// Next, we should be able to find this slice referenced in the export's resources.

	res := findResourceSchemaByCachedResourceEndpointSlice(export, endpointSlice)
	if res == nil {
		logger.Error(nil, "APIExport doesn't export this CachedResourceEndpointSlice")
		return nil
	}

	// Get the schema, and check that this actually belongs to the GVR of this CachedResource.

	sch, err := c.getAPIResourceSchema(logicalcluster.From(export), res.Schema)
	if err != nil {
		logger.Error(err, "failed to get APIResourceSchema for the APIExport")
		return err
	}

	gvr := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)

	hasVersionMatch := false
	for i := range sch.Spec.Versions {
		if sch.Spec.Versions[i].Served && sch.Spec.Versions[i].Name == gvr.Version {
			hasVersionMatch = true
			break
		}
	}
	if !hasVersionMatch {
		logger.Error(nil, "referenced APIResourceSchema doesn't serve required version", "gvr", gvr.String())
		return fmt.Errorf("APIResourceSchema %s|%s doesn't serve %s", logicalcluster.From(sch), sch.Name, gvr)
	}

	logger.Info("creating API definition", "gvr", gvr)
	apiDefinition, err := c.createAPIDefinition(sch, cachedResource, export)
	if err != nil {
		// TODO(ncdc): would be nice to expose some sort of user-visible error
		logger.Error(err, "error creating api definition", "gvr", gvr)
		return err
	}

	apiSet := make(apidefinition.APIDefinitionSet)
	for _, version := range sch.Spec.Versions {
		if !version.Served {
			continue
		}
		apiSet[gvr.GroupResource().WithVersion(version.Name)] = apiResourceSchemaApiDefinition{
			APIDefinition: apiDefinition,
			UID:           sch.UID,
			IdentityHash:  cachedResource.Status.IdentityHash,
		}
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.apiSets[apiDomainKey] = apiSet
	return nil
}

type apiResourceSchemaApiDefinition struct {
	apidefinition.APIDefinition

	UID          types.UID
	IdentityHash string
}
