/*
Copyright 2023 The KCP Authors.

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

package partitionset

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	topologyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/topology/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
)

const indexPartitionsByPartitionSet = "indexPartitionsByPartitionSet"

// indexPartitionsByPartitionSetFunc is an index function that maps a Partition to the key for its
// PartitionSet.
func indexPartitionsByPartitionSetFunc(obj interface{}) ([]string, error) {
	partition, ok := obj.(*topologyv1alpha1.Partition)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a Partition, but is %T", obj)
	}

	for _, ownerRef := range partition.OwnerReferences {
		if ownerRef.Kind == "PartitionSet" {
			path := logicalcluster.From(partition).Path()
			key := client.ToClusterAwareKey(path, ownerRef.Name)
			return []string{key}, nil
		}
	}
	return []string{}, nil
}
