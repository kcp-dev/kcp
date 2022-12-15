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

package apibinding

import (
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
)

const indexAPIBindingsByWorkspaceExport = "apiBindingsByWorkspaceExport"

// indexAPIBindingsByWorkspaceExportFunc is an index function that maps an APIBinding to the key for its
// spec.reference.workspace.
func indexAPIBindingsByWorkspaceExportFunc(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIBinding, but is %T", obj)
	}

	if apiBinding.Spec.Reference.Export != nil {
		path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
		if path.Empty() {
			path = logicalcluster.From(apiBinding).Path()
		}
		key := kcpcache.ToClusterAwareKey(path.String(), "", apiBinding.Spec.Reference.Export.Name)
		return []string{key}, nil
	}

	return []string{}, nil
}

const indexAPIExportsByAPIResourceSchema = "apiExportsByAPIResourceSchema"

// indexAPIExportsByAPIResourceSchemasFunc is an index function that maps an APIExport to its spec.latestResourceSchemas.
func indexAPIExportsByAPIResourceSchemasFunc(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExport, but is %T", obj)
	}

	ret := make([]string, len(apiExport.Spec.LatestResourceSchemas))
	for i := range apiExport.Spec.LatestResourceSchemas {
		ret[i] = client.ToClusterAwareKey(logicalcluster.From(apiExport).Path(), apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}
