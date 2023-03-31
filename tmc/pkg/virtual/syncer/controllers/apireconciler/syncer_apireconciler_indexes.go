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
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/client"
)

// IndexAPIExportsByAPIResourceSchemas is an index function that maps an APIExport to its spec.latestResourceSchemas.
func IndexAPIExportsByAPIResourceSchemas(obj interface{}) ([]string, error) {
	apiExport := obj.(*apisv1alpha1.APIExport)

	ret := make([]string, len(apiExport.Spec.LatestResourceSchemas))
	for i := range apiExport.Spec.LatestResourceSchemas {
		ret[i] = client.ToClusterAwareKey(logicalcluster.From(apiExport).Path(), apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}

func IndexSyncTargetsByExports(obj interface{}) ([]string, error) {
	syncTarget := obj.(*workloadv1alpha1.SyncTarget)

	clusterName := logicalcluster.From(syncTarget)
	keys := make([]string, 0, len(syncTarget.Spec.SupportedAPIExports))
	for _, export := range syncTarget.Spec.SupportedAPIExports {
		path := export.Path
		if path == "" {
			path = clusterName.String()
		}
		keys = append(keys, client.ToClusterAwareKey(logicalcluster.NewPath(path), export.Export))
	}

	return keys, nil
}
