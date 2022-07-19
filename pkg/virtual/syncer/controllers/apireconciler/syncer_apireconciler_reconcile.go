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

func (c *APIReconciler) reconcile(ctx context.Context, apiExport *apisv1alpha1.APIExport, apiDomainKey dynamiccontext.APIDomainKey, syncTargetName string) error {
	if apiExport == nil || apiExport.Status.IdentityHash == "" {
		// new APIExport that is not ready yet, or export got deleted

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

	// collect APIResourceSchemas
	schemaIdentity := map[string]string{}
	apiResourceSchemas := make([]*apisv1alpha1.APIResourceSchema, 0, len(apiExport.Spec.LatestResourceSchemas)+len(internalapis.Schemas))
	for _, schema := range internalapis.Schemas {
		shallow := *schema
		shallow.ClusterName = logicalcluster.From(apiExport).String() // intentionally no direct assignment
		apiResourceSchemas = append(apiResourceSchemas, &shallow)
	}
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiResourceSchema, err := c.apiResourceSchemaLister.Get(clusters.ToClusterAwareKey(logicalcluster.From(apiExport), schemaName))
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if apierrors.IsNotFound(err) {
			klog.V(3).Infof("APIResourceSchema %s in APIExport %s|% not found", schemaName, apiExport.Namespace, apiExport.Name)
			continue
		}
		apiResourceSchemas = append(apiResourceSchemas, apiResourceSchema)
		schemaIdentity[apiResourceSchema.Name] = apiExport.Status.IdentityHash
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
				if oldDef.UID != apiResourceSchema.UID {
					klog.V(4).Infof("APIResourceSchema %s|%s UID has changed: %q -> %q", logicalcluster.From(apiResourceSchema), apiResourceSchema.Name, oldDef.UID, apiResourceSchema.UID)
				}
				if oldDef.IdentityHash != schemaIdentity[apiResourceSchema.Name] {
					klog.V(4).Infof("APIResourceSchema %s|%s identity hash has changed: %q -> %q", logicalcluster.From(apiResourceSchema), apiResourceSchema.Name, oldDef.IdentityHash, schemaIdentity[apiResourceSchema.Name])
				}
				if oldDef.UID == apiResourceSchema.UID && oldDef.IdentityHash == schemaIdentity[apiResourceSchema.Name] {
					// this is the same schema and identity as before. no need to update.
					newSet[gvr] = oldDef
					preservedGVR = append(preservedGVR, gvrString(gvr))
					continue
				}
			}

			apiDefinition, err := c.createAPIDefinition(syncTargetName, apiResourceSchema, version.Name, schemaIdentity[apiResourceSchema.Name])
			if err != nil {
				klog.Errorf("failed to create API definition for %s: %v", gvr, err)
				continue
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: apiDefinition,
				UID:           apiResourceSchema.UID,
				IdentityHash:  schemaIdentity[apiResourceSchema.Name],
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

	klog.V(2).Infof("Updating APIs for APIExport %s|%s and APIDomainKey %s: new=%v, preserved=%v, removed=%v", logicalcluster.From(apiExport), apiExport.Name, apiDomainKey, newGVRs, preservedGVR, removedGVRs)

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
