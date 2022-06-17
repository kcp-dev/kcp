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
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/internalapis"
)

const (
	// Copy because  of circle imports
	indexAPIExportByIdentity = "byIdentity"
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

	// Get schemas and identities for base api export
	apiResourceSchemas, schemaIdentity, err := c.getSchemasFromAPIExport(apiExport)
	if err != nil {
		return err
	}

	for _, pc := range apiExport.Spec.PermissionClaims {
		if pc.IdentityHash != "" {
			exports, err := c.apiExportIndexer.ByIndex(indexAPIExportByIdentity, pc.IdentityHash)
			if err != nil {
				return err
			}
			for _, export := range exports {
				e, ok := export.(*apisv1alpha1.APIExport)
				e = e.DeepCopy()
				if !ok {
					return fmt.Errorf("invalid type from apiExportIndexer got: %T, expected %T", export, apiExport)
				}
				schemas, schemaIDs, err := c.getSchemasFromAPIExport(e)
				if err != nil {
					return err
				}
				apiResourceSchemas = append(apiResourceSchemas, schemas...)
				for k, v := range schemaIDs {
					// Override the permission claims that should be used. for this schema identity
					v.Spec.PermissionClaims = apiExport.Spec.PermissionClaims
					schemaIdentity[k] = v
				}
			}
		} else {
			for _, schema := range internalapis.Schemas {
				if pc.GroupResource.Group == schema.Spec.Group && pc.GroupResource.Resource == schema.Spec.Names.Plural {
					shallow := *schema
					shallow.ClusterName = logicalcluster.From(apiExport).String()
					apiResourceSchemas = append(apiResourceSchemas, &shallow)
					schemaIdentity[shallow.Name] = apiExport
				}
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

			apiDefinition, err := c.createAPIDefinition(apiResourceSchema, version.Name, schemaIdentity[apiResourceSchema.Name])
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

func (c *APIReconciler) getSchemasFromAPIExport(apiExport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIResourceSchema, map[string]*apisv1alpha1.APIExport, error) {
	apiResourceSchemas := []*apisv1alpha1.APIResourceSchema{}
	schemaIdentity := map[string]*apisv1alpha1.APIExport{}
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
		schemaIdentity[schemaName] = apiExport
	}

	return apiResourceSchemas, schemaIdentity, nil
}
