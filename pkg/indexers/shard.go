/*
Copyright 2026 The kcp Authors.

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

package indexers

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
)

// ShardByName returns the Shard with the given name, regardless of the logical
// cluster it lives in.
func ShardByName(lister corev1alpha1listers.ShardClusterLister, name string) (*corev1alpha1.Shard, error) {
	shards, err := lister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	var found *corev1alpha1.Shard
	for _, shard := range shards {
		if shard.Name != name {
			continue
		}
		if logicalcluster.From(shard) == configshard.SystemShardCluster {
			return shard, nil
		}
		found = shard
	}
	if found == nil {
		return nil, errors.NewNotFound(corev1alpha1.Resource("shards"), name)
	}
	return found, nil
}
