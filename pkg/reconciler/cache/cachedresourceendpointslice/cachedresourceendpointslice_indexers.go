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
)

const (
	byCachedResourceAndLogicalCluster = "byCachedResourceAndLogicalCluster"
)

// indexCachedResourceEndpointSliceByCachedResourceAndLogicalCluster is an index function that maps a reference to CachedResource and its cluster to a key.
func indexCachedResourceEndpointSliceByCachedResourceAndLogicalCluster(obj interface{}) ([]string, error) {
	endpoints, ok := obj.(*cachev1alpha1.CachedResourceEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be CachedResourceEndpointSlice, but is %T", obj)
	}

	key := cachedResourceEndpointSliceByCachedResourceAndLogicalCluster(&endpoints.Spec.CachedResource, logicalcluster.From(endpoints))
	return []string{key}, nil
}

func cachedResourceEndpointSliceByCachedResourceAndLogicalCluster(cachedResourceRef *cachev1alpha1.CachedResourceReference, cluster logicalcluster.Name) string {
	var key string
	key += cachedResourceRef.Name
	if !cluster.Empty() {
		key += "|" + cluster.String()
	}
	return key
}
