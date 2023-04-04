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
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas"
	apiexportbuiltin "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1/permissionclaims"
)

func (c *APIReconciler) reconcile(ctx context.Context, apiExport *apisv1alpha1.APIExport, apiDomainKey dynamiccontext.APIDomainKey) error {
	logger := klog.FromContext(ctx)
	ctx = klog.NewContext(ctx, logger)

	if apiExport == nil || apiExport.Status.IdentityHash == "" {
		c.mutex.RLock()
		_, found := c.apiSets[apiDomainKey]
		c.mutex.RUnlock()

		if !found {
			logger.V(3).Info("no APIs found for API domain key")
			return nil
		}

		// remove the APIDomain
		c.mutex.Lock()
		defer c.mutex.Unlock()
		logger.V(2).Info("deleting APIs for API domain key")
		delete(c.apiSets, apiDomainKey)
		return nil
	}

	c.mutex.RLock()
	oldSet := c.apiSets[apiDomainKey]
	c.mutex.RUnlock()

	// Get schemas and identities for base api export.
	apiResourceSchemas, err := c.getSchemasFromAPIExport(ctx, apiExport)
	if err != nil {
		return err
	}
	identities := map[schema.GroupResource]string{}
	for gr := range apiResourceSchemas {
		identities[gr] = apiExport.Status.IdentityHash
	}

	clusterName := logicalcluster.From(apiExport)

	// Find schemas for claimed resources
	claims := map[schema.GroupResource]apisv1alpha1.PermissionClaim{}
	claimsAPIBindings := false
	for _, pc := range apiExport.Spec.PermissionClaims {
		logger := logger.WithValues("claim", pc.String())
		logger.V(4).Info("evaluating claim")

		// APIExport resources have priority over claimed resources
		gr := schema.GroupResource{Group: pc.Group, Resource: pc.Resource}
		if _, found := apiResourceSchemas[gr]; found {
			if otherClaim, found := claims[gr]; found {
				logger.Info("permission claim is shadowed by another claim", "claim", pc, "otherClaim", otherClaim)
				continue
			}

			logger.Info("permission claim is shadowed by exported resource", "claim", pc)
			continue
		}

		// internal APIs have no identity and a fixed schema.
		if apiexportbuiltin.IsBuiltInAPI(pc.GroupResource) {
			internalSchema, err := apiexportbuiltin.GetBuiltInAPISchema(pc.GroupResource)
			if err != nil {
				return err
			}
			shallow := *internalSchema
			if shallow.Annotations == nil {
				shallow.Annotations = make(map[string]string)
			}
			shallow.Annotations[logicalcluster.AnnotationKey] = clusterName.String()
			apiResourceSchemas[gr] = &shallow
			claims[gr] = pc
			continue
		}
		if pc.Group == apis.GroupName {
			apisSchema, found := schemas.ApisKcpDevSchemas[pc.Resource]
			if !found {
				logger.Info("permission claim is for an unknown resource", "claim", pc)
				continue
			}

			if pc.Resource == "apibindings" {
				claimsAPIBindings = true
			}

			apiResourceSchemas[gr] = apisSchema
			claims[gr] = pc
			continue
		}
		if pc.IdentityHash == "" {
			// NOTE(hasheddan): this is checked by admission so we should never
			// hit this case.
			logger.Info("permission claim is not internal and does not have an identity hash", "claim", pc)
			continue
		}

		logger = logger.WithValues("identity", pc.IdentityHash)

		logger.V(4).Info("getting APIExports by identity")
		exports, err := c.apiExportIndexer.ByIndex(indexers.APIExportByIdentity, pc.IdentityHash)
		if err != nil {
			return err
		}

		logger.V(4).Info("got APIExports", "count", len(exports))

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
			logger := logger.WithValues(logging.FromPrefix("candidateAPIExport", export)...)
			logger.V(4).Info("getting APIResourceSchemas for candidate APIExport")
			candidates, err := c.getSchemasFromAPIExport(ctx, export)
			if err != nil {
				return err
			}
			logger.V(4).Info("got APIResourceSchemas for candidate APIExport", "count", len(candidates))
			for _, apiResourceSchema := range candidates {
				logger := logger.WithValues(logging.FromPrefix("candidateAPIResourceSchema", apiResourceSchema)...)
				logger = logger.WithValues("candidateGroup", apiResourceSchema.Spec.Group, "candidateResource", apiResourceSchema.Spec.Names.Plural)
				logger.V(4).Info("evaluating candidate APIResourceSchema")
				if apiResourceSchema.Spec.Group != pc.Group || apiResourceSchema.Spec.Names.Plural != pc.Resource {
					logger.V(4).Info("not a match")
					continue
				}
				logger.V(4).Info("got a match!")
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

			var labelReqs labels.Requirements
			newClaimLabels := ""
			if c, ok := claims[gvr.GroupResource()]; ok {
				key, label, err := permissionclaims.ToLabelKeyAndValue(clusterName, apiExport.Name, c)
				if err != nil {
					return fmt.Errorf("failed to convert permission claim %v to label key and value: %w", c, err)
				}
				claimLabels := []string{label}
				if gvr.GroupResource() == apisv1alpha1.Resource("apibindings") {
					_, fallbackLabel := permissionclaims.ToReflexiveAPIBindingLabelKeyAndValue(logicalcluster.From(apiExport), apiExport.Name)
					claimLabels = append(claimLabels, fallbackLabel)
				}
				req, err := labels.NewRequirement(key, selection.In, claimLabels)
				if err != nil {
					return fmt.Errorf("failed to create label requirement for permission claim %v: %w", c, err)
				}
				labelReqs = labels.Requirements{*req}
				newClaimLabels = strings.Join(claimLabels, "|")
			}

			oldDef, found := oldSet[gvr]
			if found {
				oldDef := oldDef.(apiResourceSchemaApiDefinition)
				if oldDef.UID == apiResourceSchema.UID && oldDef.IdentityHash == apiExport.Status.IdentityHash && oldDef.ClaimLabels == newClaimLabels {
					// this is the same schema + identity + claim labels as before. no need to update.
					newSet[gvr] = oldDef
					preservedGVR = append(preservedGVR, gvrString(gvr))
					continue
				}
			}

			logger.Info("creating API definition", "gvr", gvr, "labels", labelReqs)
			apiDefinition, err := c.createAPIDefinition(apiResourceSchema, version.Name, identities[gvr.GroupResource()], labelReqs)
			if err != nil {
				// TODO(ncdc): would be nice to expose some sort of user-visible error
				logger.Error(err, "error creating api definition", "gvr", gvr)
				continue
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: apiDefinition,
				UID:           apiResourceSchema.UID,
				IdentityHash:  apiExport.Status.IdentityHash,
				ClaimLabels:   newClaimLabels,
			}
			newGVRs = append(newGVRs, gvrString(gvr))
		}
	}

	if !claimsAPIBindings {
		d, err := c.createAPIBindingAPIDefinition(ctx, clusterName, apiExport.Name)
		if err != nil {
			// TODO(ncdc): would be nice to expose some sort of user-visible error
			logger.Error(err, "error creating api definition for apibindings")
		}

		gvr := apisv1alpha1.SchemeGroupVersion.WithResource("apibindings")
		newSet[gvr] = apiResourceSchemaApiDefinition{
			APIDefinition: d,
		}
		newGVRs = append(newGVRs, gvrString(gvr))
	}

	// cleanup old definitions
	removedGVRs := []string{}
	for gvr, oldDef := range oldSet {
		if newDef, found := newSet[gvr]; !found || oldDef.(apiResourceSchemaApiDefinition).APIDefinition != newDef.(apiResourceSchemaApiDefinition).APIDefinition {
			removedGVRs = append(removedGVRs, gvrString(gvr))
			oldDef.TearDown()
		}
	}

	logger.V(2).Info("updating APIs", "new", newGVRs, "preserved", preservedGVR, "removed", removedGVRs)

	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.apiSets[apiDomainKey] = newSet

	return nil
}

type apiResourceSchemaApiDefinition struct {
	apidefinition.APIDefinition

	UID          types.UID
	IdentityHash string
	ClaimLabels  string
}

func gvrString(gvr schema.GroupVersionResource) string {
	group := gvr.Group
	if group == "" {
		group = "core"
	}
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Version, group)
}

func (c *APIReconciler) getSchemasFromAPIExport(ctx context.Context, apiExport *apisv1alpha1.APIExport) (map[schema.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	logger := klog.FromContext(ctx)
	apiResourceSchemas := map[schema.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiExportClusterName := logicalcluster.From(apiExport)
		apiResourceSchema, err := c.apiResourceSchemaLister.Cluster(apiExportClusterName).Get(schemaName)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if apierrors.IsNotFound(err) {
			logger.WithValues(
				"schema", schemaName,
				"exportClusterName", apiExportClusterName,
				"exportName", apiExport.Name,
			).V(3).Info("APIResourceSchema for APIExport not found")
			continue
		}
		apiResourceSchemas[schema.GroupResource{Group: apiResourceSchema.Spec.Group, Resource: apiResourceSchema.Spec.Names.Plural}] = apiResourceSchema
	}

	return apiResourceSchemas, nil
}
