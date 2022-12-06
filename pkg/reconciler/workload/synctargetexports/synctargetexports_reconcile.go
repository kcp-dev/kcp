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
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// exportReconciler updates syncedResource in SyncTarget status based on supportedAPIExports.
type exportReconciler struct {
	getAPIExport      func(path logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	getResourceSchema func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func (e *exportReconciler) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (*workloadv1alpha1.SyncTarget, error) {
	var errs []error
	var syncedResources []workloadv1alpha1.ResourceToSync
	for _, exportRef := range syncTarget.Spec.SupportedAPIExports {
		export, err := e.getAPIExport(logicalcluster.New(exportRef.Path), exportRef.Export)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}

		for _, schema := range export.Spec.LatestResourceSchemas {
			syncedResource, err := e.convertSchemaToSyncedResource(tenancy.From(export), schema, export.Status.IdentityHash)
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
	return syncTarget, errors.NewAggregate(errs)
}

func (e *exportReconciler) convertSchemaToSyncedResource(clusterName tenancy.Cluster, schemaName, identityHash string) (workloadv1alpha1.ResourceToSync, error) {
	schema, err := e.getResourceSchema(clusterName, schemaName)
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
