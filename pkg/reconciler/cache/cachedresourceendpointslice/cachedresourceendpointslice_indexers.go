/*
Copyright 2025 The KCP Authors.

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

package cachedresourceendpointslice

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/client"
)

const (
	indexCachedResourceEndpointSlicesByPartition = "indexCachedResourceEndpointSlicesByPartition"

	IndexCachedResourceEndpointSliceByCachedResource = "IndexCachedResourceEndpointSliceByCachedResource"
)

// indexCachedResourceEndpointSlicesByPartitionFunc is an index function that maps a Partition to the key for its
// spec.partition.
func indexCachedResourceEndpointSlicesByPartitionFunc(obj interface{}) ([]string, error) {
	slice, ok := obj.(*cachev1alpha1.CachedResourceEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an CachedResourceEndpointSlice, but is %T", obj)
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

// IndexCachedResourceEndpointSliceByCachedResourceFunc is an index function that indexes
// a CachedResourceEndpointSlice by the CachedResource it references.
func IndexCachedResourceEndpointSliceByCachedResourceFunc(obj interface{}) ([]string, error) {
	slice, ok := obj.(*cachev1alpha1.CachedResourceEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an CachedResourceEndpointSlice", obj)
	}

	pathLocal := logicalcluster.From(slice).Path()
	// TODO(gman0): add an optional external path index key once we add "CachedResourceEndpointSlice.spec.cachedResource.path".
	return []string{pathLocal.Join(slice.Spec.CachedResource.Name).String()}, nil
}
