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

	"github.com/kcp-dev/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
)

type exportAndClaimInfo struct {
	identityHash string
	claim        *apisv1alpha1.PermissionClaim
}

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

	// Get schemas and identities for base api export
	// schemaIdentity will map the schema to the given API export that it comes from.
	// and potentially a
	apiResourceSchemas, schemaIdentity, err := c.getSchemasFromAPIExport(apiExport)
	if err != nil {
		return err
	}

	for _, pc := range apiExport.Spec.PermissionClaims {
		internal, schema := isPermissionClaimForInternalAPI(pc)
		if internal {
			shallow := *schema
			shallow.ClusterName = logicalcluster.From(apiExport).String()
			apiResourceSchemas = append(apiResourceSchemas, &shallow)
			schemaIdentity[shallow.Name] = exportAndClaimInfo{}
			continue
		}
		if !internal && pc.IdentityHash != "" {
			exports, err := c.apiExportIndexer.ByIndex(indexers.IndexAPIExportByIdentity, pc.IdentityHash)
			if err != nil {
				return err
			}
			if len(exports) != 1 {
				return fmt.Errorf("got %d api exports expected one", len(exports))
			}
			export := exports[0]
			// For the export that si found based on an the identity hash
			// we need to add all the latest schemas that the export provides
			// We all need to map the schema to the given APIExport that it comes from
			// so that when creating the api definition for it, we can use the given
			// api export to fully fill in and serve the API for the virtual workspace.
			owningExport, ok := export.(*apisv1alpha1.APIExport)
			if !ok {
				return fmt.Errorf("invalid type from apiExportIndexer got: %T, expected %T", export, apiExport)
			}
			schemas, schemaIDs, err := c.getSchemasFromAPIExport(owningExport)
			if err != nil {
				return err
			}
			for _, schema := range schemas {
				if schema.Spec.Group != pc.Group || schema.Spec.Names.Plural != pc.Resource {
					continue
				}
				apiResourceSchemas = append(apiResourceSchemas, schema)
			}
			for k := range schemaIDs {
				// Override the permission claims that should be used. for this schema identity
				// Here we will set the identity for the schema for a permission claim.
				exportAndClaim := schemaIdentity[k]
				exportAndClaim.claim = &pc
				exportAndClaim.identityHash = owningExport.Status.IdentityHash
				schemaIdentity[k] = exportAndClaim
			}
			continue
		}
		// TODO: Add a openapi validation to catch this case, and it should never happen
		klog.Warningf("Permission claim %v for APIEXport %s is not internal and does not have an identity hash",
			pc,
			fmt.Sprintf("%s|%s", logicalcluster.From(apiExport), apiExport.Name),
		)
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

			apiExportAndClaim := schemaIdentity[apiResourceSchema.Name]
			apiDefinition, err := c.createAPIDefinition(apiResourceSchema, version.Name, apiExportAndClaim.identityHash, apiExportAndClaim.claim)
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

func (c *APIReconciler) getSchemasFromAPIExport(apiExport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIResourceSchema, map[string]exportAndClaimInfo, error) {
	apiResourceSchemas := []*apisv1alpha1.APIResourceSchema{}
	schemaIdentity := map[string]exportAndClaimInfo{}
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiResourceSchema, err := c.apiResourceSchemaLister.Get(clusters.ToClusterAwareKey(logicalcluster.From(apiExport), schemaName))
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		if apierrors.IsNotFound(err) {
			klog.V(3).Infof("APIResourceSchema %s in APIExport %s|% not found", schemaName, apiExport.Namespace, apiExport.Name)
			continue
		}
		apiResourceSchemas = append(apiResourceSchemas, apiResourceSchema)
		// We may need to change/mutate this API Export to get the permission claims.
		schemaIdentity[schemaName] = exportAndClaimInfo{
			identityHash: apiExport.Status.IdentityHash,
		}
	}

	return apiResourceSchemas, schemaIdentity, nil
}

func isPermissionClaimForInternalAPI(claim apisv1alpha1.PermissionClaim) (bool, *apisv1alpha1.APIResourceSchema) {
	for _, schema := range internalapis.Schemas {
		if claim.GroupResource.Group == schema.Spec.Group && claim.GroupResource.Resource == schema.Spec.Names.Plural {
			return true, schema
		}
	}
	return false, nil
}
