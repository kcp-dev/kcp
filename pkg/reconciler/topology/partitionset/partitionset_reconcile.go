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
	"context"
	"sort"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, partitionSet *topologyv1alpha1.PartitionSet) error {
	logger := klog.FromContext(ctx)
	logger = logging.WithObject(logger, partitionSet)
	labelSelector := labels.Everything()
	if partitionSet.Spec.ShardSelector != nil {
		var err error
		labelSelector, err = metav1.LabelSelectorAsSelector(partitionSet.Spec.ShardSelector)
		// This should not happen due to OpenAPI validation
		if err != nil {
			conditions.MarkFalse(
				partitionSet,
				topologyv1alpha1.PartitionSetValid,
				topologyv1alpha1.PartitionSetInvalidSelectorReason,
				conditionsv1alpha1.ConditionSeverityError,
				err.Error(),
			)
			conditions.MarkFalse(
				partitionSet,
				topologyv1alpha1.PartitionsReady,
				topologyv1alpha1.ErrorGeneratingPartitionsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"",
			)
			// No need to requeue if the selector is not valid
			return nil
		}
	}
	conditions.MarkTrue(partitionSet, topologyv1alpha1.PartitionSetValid)

	shards, err := c.listShards(labelSelector)
	if err != nil {
		conditions.MarkFalse(
			partitionSet,
			topologyv1alpha1.PartitionsReady,
			topologyv1alpha1.ErrorGeneratingPartitionsReason,
			conditionsv1alpha1.ConditionSeverityError,
			"error listing Shards",
		)
		return err
	}

	oldPartitions, err := c.getPartitionsByPartitionSet(ctx, partitionSet)
	if err != nil {
		conditions.MarkFalse(
			partitionSet,
			topologyv1alpha1.PartitionsReady,
			topologyv1alpha1.ErrorGeneratingPartitionsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
		return err
	}

	var matchLabelsMap map[string]map[string]string
	// remove duplicates
	dimensions := sets.List[string](sets.New[string](partitionSet.Spec.Dimensions...))
	if partitionSet.Spec.ShardSelector != nil {
		matchLabelsMap = partition(shards, dimensions, partitionSet.Spec.ShardSelector.MatchLabels)
	} else {
		matchLabelsMap = partition(shards, dimensions, nil)
	}
	partitionSet.Status.Count = uint16(len(matchLabelsMap))
	existingMatches := map[string]struct{}{}
	newMatchExpressions := []metav1.LabelSelectorRequirement{}
	if partitionSet.Spec.ShardSelector != nil {
		newMatchExpressions = partitionSet.Spec.ShardSelector.MatchExpressions
	}
	// loop through existing partitions and delete old partitions owned by the PartitionSet that are no match anymore
	// store existing matches for not to recreate existing Partitions
	for _, oldPartition := range oldPartitions {
		pLogger := logging.WithObject(logger, oldPartition)
		// MatchExpressions need to be the same
		oldMatchExpressions := []metav1.LabelSelectorRequirement{}
		if oldPartition.Spec.Selector != nil {
			oldMatchExpressions = oldPartition.Spec.Selector.MatchExpressions
		}
		if !equality.Semantic.DeepEqual(oldMatchExpressions, newMatchExpressions) {
			pLogger.V(2).Info("deleting partition")
			if err := c.deletePartition(ctx, logicalcluster.From(oldPartition).Path(), oldPartition.Name); err != nil && !apierrors.IsNotFound(err) {
				conditions.MarkFalse(
					partitionSet,
					topologyv1alpha1.PartitionsReady,
					topologyv1alpha1.ErrorGeneratingPartitionsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"old partition could not get deleted",
				)
				return err
			}
			continue
		}

		// MatchLabels need to be the same
		partitionKey := ""
		oldMatchLabels := map[string]string{}
		if oldPartition.Spec.Selector != nil {
			oldMatchLabels = oldPartition.Spec.Selector.MatchLabels
		}

		// Sorting the keys of the old partition for consistent comparison
		sortedOldKeys := make([]string, len(oldMatchLabels))
		i := 0
		for k := range oldMatchLabels {
			sortedOldKeys[i] = k
			i++
		}
		sort.Strings(sortedOldKeys)
		for _, key := range sortedOldKeys {
			partitionKey = partitionKey + "+" + key + "=" + oldMatchLabels[key]
		}
		if _, ok := matchLabelsMap[partitionKey]; ok {
			existingMatches[partitionKey] = struct{}{}
		} else {
			pLogger.V(2).Info("deleting partition")
			if err := c.deletePartition(ctx, logicalcluster.From(oldPartition).Path(), oldPartition.Name); err != nil && !apierrors.IsNotFound(err) {
				conditions.MarkFalse(
					partitionSet,
					topologyv1alpha1.PartitionsReady,
					topologyv1alpha1.ErrorGeneratingPartitionsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"old partition could not get deleted",
				)
				return err
			}
		}
	}

	// Create partitions when no existing partition for the set has the same selector.
	for key, matchLabels := range matchLabelsMap {
		if _, ok := existingMatches[key]; !ok {
			partition := generatePartition(partitionSet.Name, newMatchExpressions, matchLabels, dimensions)
			partition.OwnerReferences = []metav1.OwnerReference{
				*metav1.NewControllerRef(partitionSet, topologyv1alpha1.SchemeGroupVersion.WithKind("PartitionSet")),
			}
			pLogger := logging.WithObject(logger, partition)
			pLogger.V(2).Info("creating partition")
			_, err = c.createPartition(ctx, logicalcluster.From(partitionSet).Path(), partition)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				conditions.MarkFalse(
					partitionSet,
					topologyv1alpha1.PartitionsReady,
					topologyv1alpha1.ErrorGeneratingPartitionsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"partition could not get created",
				)
				return err
			}
		}
	}
	conditions.MarkTrue(partitionSet, topologyv1alpha1.PartitionsReady)
	return nil
}

// partition populates shard label selectors according to dimensions.
// It only keeps selectors that have at least one Shard matching them
// so that Partitions not referring to any Shard would not get created.
func partition(shards []*corev1alpha1.Shard, dimensions []string, shardSelectorLabels map[string]string) (matchLabelsMap map[string]map[string]string) {
	matchLabelsMap = make(map[string]map[string]string)
	labels := make([]string, len(dimensions), len(dimensions)+len(shardSelectorLabels))
	copy(labels, dimensions)
	for label := range shardSelectorLabels {
		labels = append(labels, label)
	}
	sort.Strings(labels) // Sorting for consistent comparison.
	for _, shard := range shards {
		key := ""
		selector := make(map[string]string)
		matchingLabels := true
		for _, label := range labels {
			labelValue, ok := shard.Labels[label]
			if !ok {
				matchingLabels = false
				break
			}
			key = key + "+" + label + "=" + labelValue
			selector[label] = labelValue
		}
		if matchingLabels && len(key) > 0 {
			matchLabelsMap[key] = selector
		}
	}
	return matchLabelsMap
}
