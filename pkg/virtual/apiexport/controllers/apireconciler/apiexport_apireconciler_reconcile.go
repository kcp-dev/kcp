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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
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
		internal, apiResourceSchema := c.isPermissionClaimForInternalAPI(pc)
		if internal {
			shallow := *apiResourceSchema
			// nolint:staticcheck
			shallow.ZZZ_DeprecatedClusterName = logicalcluster.From(apiExport).String()
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

func (c *APIReconciler) isPermissionClaimForInternalAPI(claim *apisv1alpha1.PermissionClaim) (bool, *apisv1alpha1.APIResourceSchema) {
	for _, schema := range c.internalAPIResourceSchemas {
		if claim.GroupResource.Group == schema.Spec.Group && claim.GroupResource.Resource == schema.Spec.Names.Plural {
			return true, schema
		}
	}
	return false, nil
}
