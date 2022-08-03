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
	"sort"

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (*workloadv1alpha1.SyncTarget, reconcileStatus, error)
}

// exportReconciler updates syncedResource in SyncTarget status based on supporteAPIExports.
type exportReconciler struct {
	getAPIExport      func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	getResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func (e *exportReconciler) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (*workloadv1alpha1.SyncTarget, reconcileStatus, error) {
	exportKeys := getExportKeys(syncTarget)

	var errs []error
	var syncedResources []workloadv1alpha1.ResourceToSync
	for _, exportKey := range exportKeys {
		exportCluster, name := clusters.SplitClusterAwareKey(exportKey)
		export, err := e.getAPIExport(exportCluster, name)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}

		for _, schema := range export.Spec.LatestResourceSchemas {
			syncedResource, err := e.convertSchemaToSyncedResource(exportCluster, schema, export.Status.IdentityHash)
			if err != nil {
				klog.Warningf("cannot get schema: %v", err)
				continue
			}
			syncedResources = append(syncedResources, syncedResource)
		}
	}

	// sort synced resource by group
	sort.SliceStable(syncedResources, func(i, j int) bool {
		if syncedResources[i].Group > syncedResources[j].Group {
			return true
		}

		if syncedResources[i].Resource > syncedResources[j].Resource {
			return true
		}
		return false
	})

	// merge synced resource using desired as base and update it state based on existing synced.
	for _, existingSynced := range syncTarget.Status.SyncedResources {
		for i := range syncedResources {
			if syncedResources[i].GroupResource == existingSynced.GroupResource && syncedResources[i].IdentityHash == existingSynced.IdentityHash {
				syncedResources[i].State = existingSynced.State
				break
			}
		}
	}

	syncTarget.Status.SyncedResources = syncedResources
	return syncTarget, reconcileStatusContinue, errors.NewAggregate(errs)
}

func (e *exportReconciler) convertSchemaToSyncedResource(cluterName logicalcluster.Name, schemaName, identityHash string) (workloadv1alpha1.ResourceToSync, error) {
	schema, err := e.getResourceSchema(cluterName, schemaName)
	if err != nil {
		return workloadv1alpha1.ResourceToSync{}, err
	}

	syncedResource := workloadv1alpha1.ResourceToSync{
		GroupResource: apisv1alpha1.GroupResource{
			Group:    schema.Spec.Group,
			Resource: schema.Spec.Names.Plural,
		},
		Versions:     []string{},
		IdentityHash: identityHash,
	}

	for _, version := range schema.Spec.Versions {
		if version.Served {
			syncedResource.Versions = append(syncedResource.Versions, version.Name)
		}
	}
	sort.Strings(syncedResource.Versions)

	return syncedResource, nil
}

func (c *Controller) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (*workloadv1alpha1.SyncTarget, error) {
	reconcilers := []reconciler{
		&exportReconciler{
			getAPIExport:      c.getAPIExport,
			getResourceSchema: c.getResourceSchema,
		},
		&apiCompatibleReconciler{
			getAPIExport:           c.getAPIExport,
			getResourceSchema:      c.getResourceSchema,
			listAPIResourceImports: c.listAPIResourceImports,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		syncTarget, status, err = r.reconcile(ctx, syncTarget)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return syncTarget, errors.NewAggregate(errs)
}

func (c *Controller) getAPIExport(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
	key := clusters.ToClusterAwareKey(clusterName, name)
	return c.apiExportLister.Get(key)
}

func (c *Controller) getResourceSchema(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
	key := clusters.ToClusterAwareKey(clusterName, name)
	return c.resourceSchemaLister.Get(key)
}

func (c *Controller) listAPIResourceImports(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.APIResourceImport, error) {
	items, err := c.apiImportIndexer.ByIndex(indexbyWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*apiresourcev1alpha1.APIResourceImport, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*apiresourcev1alpha1.APIResourceImport))
	}
	return ret, nil
}
