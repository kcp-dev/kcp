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

package replication

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	// ByGVRAndShardAndLogicalClusterAndNamespaceAndName is the name for the index that indexes by an object's gvr, shard and logical cluster, namespace and name.
	ByGVRAndShardAndLogicalClusterAndNamespaceAndName = "kcp-byGVRAndShardAndLogicalClusterAndNamespaceAndName"

	// ByGVRAndLogicalClusterAndNamespace is the name for the index that indexes by an object's gvr, logical cluster and namespace.
	ByGVRAndLogicalClusterAndNamespace = "kcp-byGVRAndLogicalClusterAndNamespace"
)

// IndexByShardAndLogicalClusterAndNamespace is an index function that indexes by an object's shard and logical cluster, namespace and name.
func IndexByShardAndLogicalClusterAndNamespace(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	annotations := a.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	labels := a.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	gvr := schema.GroupVersionResource{
		Group:    labels[LabelKeyObjectGroup],
		Version:  labels[LabelKeyObjectVersion],
		Resource: labels[LabelKeyObjectResource],
	}
	namespace := labels[LabelKeyObjectOriginalNamespace]
	name := labels[LabelKeyObjectOriginalName]

	shardName := annotations[genericapirequest.ShardAnnotationKey]

	key := GVRAndShardAndLogicalClusterAndNamespaceKey(gvr, shardName, logicalcluster.From(a), namespace, name)
	return []string{key}, nil
}

// GVRAndShardAndLogicalClusterAndNamespaceKey creates an index key from the given parameters.
// As of today this function is used by IndexByShardAndLogicalClusterAndNamespace indexer.
// Key will be in the form of version.resource.group|shard|cluster|namespace/name.
func GVRAndShardAndLogicalClusterAndNamespaceKey(gvr schema.GroupVersionResource, shard string, cluster logicalcluster.Name, namespace, name string) string {
	var key string
	key += gvr.Version + "." + gvr.Resource + "." + gvr.Group + "|"
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

// IndexByGVRAndLogicalClusterAndNamespace is an index function that indexes by wrapped object's GVR, logical cluster and namespace.
func IndexByGVRAndLogicalClusterAndNamespace(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}

	labels := a.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	gvr := schema.GroupVersionResource{
		Group:    labels[LabelKeyObjectGroup],
		Version:  labels[LabelKeyObjectVersion],
		Resource: labels[LabelKeyObjectResource],
	}
	namespace := labels[LabelKeyObjectOriginalNamespace]

	key := GVRAndLogicalClusterAndNamespace(gvr, logicalcluster.From(a), namespace)
	return []string{key}, nil
}

// GVRAndLogicalClusterAndNamespace creates an index key from the given parameters.
func GVRAndLogicalClusterAndNamespace(gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string) string {
	var key string
	key += gvr.Version + "." + gvr.Resource + "." + gvr.Group
	if !cluster.Empty() {
		key += "|" + cluster.String()
	}
	if namespace != "" {
		key += "|" + namespace
	}
	return key
}
