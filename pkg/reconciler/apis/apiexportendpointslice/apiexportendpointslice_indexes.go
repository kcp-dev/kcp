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

package apiexportendpointslice

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/client"
)

const (
	indexAPIExportEndpointSliceByAPIExport  = "indexAPIExportEndpointSliceByAPIExport"
	indexAPIExportEndpointSlicesByPartition = "indexAPIExportEndpointSlicesByPartition"
)

// indexAPIExportEndpointSliceByAPIExportFunc indexes the APIExportEndpointSlice by their APIExport's Reference Path and Name.
func indexAPIExportEndpointSliceByAPIExportFunc(obj interface{}) ([]string, error) {
	apiExportEndpointSlice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExportEndpointSlice", obj)
	}

	path := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if path.Empty() {
		path = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	return []string{path.Join(apiExportEndpointSlice.Spec.APIExport.Name).String()}, nil
}

// indexAPIExportEndpointSlicesByPartitionFunc is an index function that maps a Partition to the key for its
// spec.partition.
func indexAPIExportEndpointSlicesByPartitionFunc(obj interface{}) ([]string, error) {
	slice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExportEndpointSlice, but is %T", obj)
	}

	if slice.Spec.Partition != "" {
		clusterName := logicalcluster.From(slice).Path()
		if !ok {
			// this will never happen due to validation
			return []string{}, fmt.Errorf("cluster information missing")
		}
		key := client.ToClusterAwareKey(clusterName, slice.Spec.Partition)
		return []string{key}, nil
	}

	return []string{}, nil
}
