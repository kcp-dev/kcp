/*
Copyright 2025 The kcp Authors.

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

package cachedresources

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

const (
	// ByGVRAndLogicalCluster is the name for the index that indexes by an object's gvr and logical cluster.
	ByGVRAndLogicalCluster = "kcp-byGVRAndLogicalCluster"
)

// IndexByShardAndLogicalClusterAndNamespace is an index function that indexes by an object's gvr and logical cluster.
func IndexByGVRAndLogicalCluster(obj interface{}) ([]string, error) {
	cachedResource := obj.(*cachev1alpha1.CachedResource)
	return []string{
		GVRAndLogicalClusterKey(
			schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource),
			logicalcluster.From(cachedResource),
		),
	}, nil
}

// GVRAndShardAndLogicalClusterAndNamespaceKey creates an index key from the given parameters.
// Key will be in the form of version.resource.group|cluster.
func GVRAndLogicalClusterKey(gvr schema.GroupVersionResource, cluster logicalcluster.Name) string {
	var key string
	if gvr.Group == "" {
		gvr.Group = "core"
	}
	key += gvr.Version + "." + gvr.Resource + "." + gvr.Group
	if !cluster.Empty() {
		key += "|" + cluster.String()
	}
	return key
}
