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

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/clusters"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

// indexAPIBindingByWorkspaceExport is an index function that maps an APIBinding to the key for its
// spec.reference.workspace.
func indexAPIBindingByWorkspaceExport(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIBinding, but is %T", obj)
	}

	if apiBinding.Spec.Reference.Workspace != nil {
		org, _, err := helper.ParseLogicalClusterName(apiBinding.ClusterName)
		if err != nil {
			return []string{}, fmt.Errorf("error parsing clusterName %q: %w", apiBinding.ClusterName, err)
		}
		apiExportClusterName := helper.EncodeOrganizationAndClusterWorkspace(org, apiBinding.Spec.Reference.Workspace.WorkspaceName)
		key := clusters.ToClusterAwareKey(apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName)
		return []string{key}, nil
	}

	return []string{}, nil
}

// indexAPIExportByAPIResourceSchemas is an index function that maps an APIExport to its spec.latestResourceSchemas.
func indexAPIExportByAPIResourceSchemas(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExport, but is %T", obj)
	}

	meta, err := meta.Accessor(obj)
	if err != nil {
		return []string{}, fmt.Errorf("object has no meta: %w", err)
	}

	clusterName := meta.GetClusterName()

	ret := make([]string, len(apiExport.Spec.LatestResourceSchemas))

	for i := range apiExport.Spec.LatestResourceSchemas {
		ret[i] = clusters.ToClusterAwareKey(clusterName, apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}
