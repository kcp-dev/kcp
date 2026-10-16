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

package coreidentityset

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	configshard "github.com/kcp-dev/kcp/config/shard"
)

// Publish creates or updates the shard's CoreIdentitySet in the shard-local
// system:shard logical cluster from the input, and returns the published
// object. The object is named after the shard. Only the owning shard writes
// it.
func Publish(ctx context.Context, client kcpclientset.ClusterInterface, in Input) (*adminv1alpha1.CoreIdentitySet, error) {
	logger := klog.FromContext(ctx).WithValues("shard", in.ShardName)
	sets := client.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().CoreIdentitySets()

	existing, err := sets.Get(ctx, in.ShardName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get CoreIdentitySet %s: %w", in.ShardName, err)
	}
	if apierrors.IsNotFound(err) {
		set := &adminv1alpha1.CoreIdentitySet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        in.ShardName,
				Annotations: map[string]string{logicalcluster.AnnotationKey: configshard.SystemShardCluster.String()},
			},
		}
		Apply(set, in)
		created, err := sets.Create(ctx, set, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create CoreIdentitySet %s: %w", in.ShardName, err)
		}
		logger.Info("created CoreIdentitySet")
		return created, nil
	}

	// the active key, once recorded, is kept: a different mounted key is
	// reported as KeyChanged, not adopted.
	if in.RecordedKeyID == "" {
		in.RecordedKeyID = existing.Status.KeyID
	}

	updated := existing.DeepCopy()
	Apply(updated, in)
	if equality.Semantic.DeepEqual(existing.Status, updated.Status) {
		return existing, nil
	}
	result, err := sets.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update CoreIdentitySet %s: %w", in.ShardName, err)
	}
	logger.V(2).Info("updated CoreIdentitySet")
	return result, nil
}
