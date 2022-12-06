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
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

// indexAPIExportsByAPIResourceSchemasFunc is an index function that maps an APIExport to its spec.latestResourceSchemas.
func indexAPIExportsByAPIResourceSchemas(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExport, but is %T", obj)
	}

	ret := make([]string, len(apiExport.Spec.LatestResourceSchemas))
	for i := range apiExport.Spec.LatestResourceSchemas {
		ret[i] = client.ToClusterAwareKey(logicalcluster.From(apiExport), apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}

func indexSyncTargetsByExports(obj interface{}) ([]string, error) {
	synctarget, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a SyncTarget, but is %T", obj)
	}

	clusterName := logicalcluster.From(synctarget)
	if len(synctarget.Spec.SupportedAPIExports) == 0 {
		return []string{client.ToClusterAwareKey(clusterName, reconcilerapiexport.TemporaryComputeServiceExportName)}, nil
	}

	var keys []string
	for _, export := range synctarget.Spec.SupportedAPIExports {
		if len(export.Path) == 0 {
			keys = append(keys, client.ToClusterAwareKey(clusterName, export.Export))
			continue
		}
		keys = append(keys, client.ToClusterAwareKey(logicalcluster.New(export.Path), export.Export))
	}

	return keys, nil
}
