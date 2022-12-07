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

package replication

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/meta"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
)

const (
	// ByShardAndLogicalClusterAndNamespaceAndName is the name for the index that indexes by an object's shard and logical cluster, namespace and name
	ByShardAndLogicalClusterAndNamespaceAndName = "kcp-byShardAndLogicalClusterAndNamespaceAndName"
)

// IndexByShardAndLogicalClusterAndNamespace is an index function that indexes by an object's shard and logical cluster, namespace and name
func IndexByShardAndLogicalClusterAndNamespace(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	annotations := a.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	// TODO: rename to genericapirequest.ShardNameAnnotationKey
	shardName := annotations[genericapirequest.AnnotationKey]

	return []string{ShardAndLogicalClusterAndNamespaceKey(shardName, logicalcluster.From(a), a.GetNamespace(), a.GetName())}, nil
}

// ShardAndLogicalClusterAndNamespaceKey creates an index key from the given parameters.
// As of today this function is used by IndexByShardAndLogicalClusterAndNamespace indexer.
func ShardAndLogicalClusterAndNamespaceKey(shard string, cluster logicalcluster.Name, namespace, name string) string {
	var key string
	if len(shard) > 0 {
		key += shard + "|"
	}
	if !cluster.Empty() {
		key += cluster.String() + "|"
	}
	if len(namespace) > 0 {
		key += namespace + "/"
	}
	if len(key) == 0 {
		return name
	}
	return fmt.Sprintf("%s%s", key, name)
}
