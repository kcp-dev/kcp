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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clusters"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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
		ret[i] = clusters.ToClusterAwareKey(logicalcluster.From(apiExport), apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}

func indexSyncTargetsByExports(obj interface{}) ([]string, error) {
	synctarget, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a SyncTarget, but is %T", obj)
	}

	return getExportKeys(synctarget), nil
}

func getExportKeys(synctarget *workloadv1alpha1.SyncTarget) []string {
	lcluster := logicalcluster.From(synctarget)
	if len(synctarget.Spec.SupportedAPIExports) == 0 {
		return []string{clusters.ToClusterAwareKey(lcluster, reconcilerapiexport.TemporaryComputeServiceExportName)}
	}

	var keys []string
	for _, export := range synctarget.Spec.SupportedAPIExports {
		if export.Workspace == nil {
			continue
		}
		if len(export.Workspace.Path) == 0 {
			keys = append(keys, clusters.ToClusterAwareKey(lcluster, export.Workspace.ExportName))
			continue
		}
		keys = append(keys, clusters.ToClusterAwareKey(logicalcluster.New(export.Workspace.Path), export.Workspace.ExportName))
	}

	return keys
}

func indexByWorksapce(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	lcluster := logicalcluster.From(metaObj)
	return []string{lcluster.String()}, nil
}
