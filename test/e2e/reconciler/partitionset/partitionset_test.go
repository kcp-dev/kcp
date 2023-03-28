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
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPartitionSet(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := framework.SharedKcpServer(t)

	// Create organization and workspace.
	// Organizations help with multiple runs.
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	partitionClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("partitionset"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	partitionSetClient := kcpClusterClient.TopologyV1alpha1().PartitionSets()
	partitionClient := kcpClusterClient.TopologyV1alpha1().Partitions()
	var partitions *topologyv1alpha1.PartitionList

	t.Logf("Creating a partitionSet not matching any shard")
	partitionSet := &topologyv1alpha1.PartitionSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-partitionset",
		},
		Spec: topologyv1alpha1.PartitionSetSpec{
			Dimensions: []string{"partition-test-region"},
			ShardSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "excluded",
						Operator: metav1.LabelSelectorOpDoesNotExist,
					},
				},
			},
		},
	}
	partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, partitionSet, metav1.CreateOptions{})
	require.NoError(t, err, "error creating partitionSet")
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionSetValid) && conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) {
			return true, ""
		}
		return false, spew.Sdump(partitionSet.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid partitionSet")
	partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error retrieving partitions")
	require.Equal(t, 0, len(partitions.Items), "no partition expected, got: %d", len(partitions.Items))

	// Newly added shards are annotated to avoid side effects on other e2e tests.
	t.Logf("Creating a shard matching the partitionSet")
	shard1a := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partition-shard-1a",
			Labels: map[string]string{
				"partition-test-region": "partition-test-region-1",
			},
			Annotations: map[string]string{
				"experimental.core.kcp.io/unschedulable": "true",
			},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL: "https://base.kcp.test.dev",
		},
	}
	shardClient := kcpClusterClient.CoreV1alpha1().Shards()
	shard1a, err = shardClient.Cluster(core.RootCluster.Path()).Create(ctx, shard1a, metav1.CreateOptions{})
	require.NoError(t, err, "error creating shard")
	// Necessary for multiple runs.
	defer func() {
		err = shardClient.Cluster(core.RootCluster.Path()).Delete(ctx, shard1a.Name, metav1.DeleteOptions{})
		require.NoError(t, err, "error deleting shard")
	}()
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) && partitionSet.Status.Count == uint16(1) {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d", partitionSet.Status.Count)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected the partition count to be 1")
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d", len(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 1 partition")
	require.Equal(t, map[string]string{"partition-test-region": "partition-test-region-1"}, partitions.Items[0].Spec.Selector.MatchLabels, "selector not as expected")

	t.Logf("Creating a shard in a second region")
	shard2 := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partition-shard-2",
			Labels: map[string]string{
				"partition-test-region": "partition-test-region-2",
			},
			Annotations: map[string]string{
				"experimental.core.kcp.io/unschedulable": "true",
			},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL: "https://base.kcp.test.dev",
		},
	}
	shard2, err = shardClient.Cluster(core.RootCluster.Path()).Create(ctx, shard2, metav1.CreateOptions{})
	// Necessary for multiple runs.
	require.NoError(t, err, "error creating shard")
	defer func() {
		err = shardClient.Cluster(core.RootCluster.Path()).Delete(ctx, shard2.Name, metav1.DeleteOptions{})
		require.NoError(t, err, "error deleting shard")
	}()
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) && partitionSet.Status.Count == uint16(2) {
			return true, ""
		}
		return false, fmt.Sprintf("expected 2 partitions, but got %d", partitionSet.Status.Count)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected the partitions to be ready and their count to be 2")
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 2 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 2 partitions, but got %d, details: %s", len(partitions.Items), spew.Sdump(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 2 partitions")
	require.True(t, (reflect.DeepEqual(partitions.Items[0].Spec.Selector.MatchLabels, map[string]string{"partition-test-region": "partition-test-region-1"}) &&
		reflect.DeepEqual(partitions.Items[1].Spec.Selector.MatchLabels, map[string]string{"partition-test-region": "partition-test-region-2"})) ||
		(reflect.DeepEqual(partitions.Items[0].Spec.Selector.MatchLabels, map[string]string{"partition-test-region": "partition-test-region-2"}) &&
			reflect.DeepEqual(partitions.Items[1].Spec.Selector.MatchLabels, map[string]string{"partition-test-region": "partition-test-region-1"})), "selectors not as expected")

	t.Logf("Moving the second shard to the same region as the first one")
	shard2.Labels = map[string]string{
		"partition-test-region": "partition-test-region-1",
	}
	_, err = shardClient.Cluster(core.RootCluster.Path()).Update(ctx, shard2, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating shard")
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) && partitionSet.Status.Count == uint16(1) {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d", partitionSet.Status.Count)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected the partition count to become 1")
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d, details: %s", len(partitions.Items), spew.Sdump(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 1 partition")

	t.Logf("Creating a shard part of a third region")
	shard3 := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partition-shard-3",
			Labels: map[string]string{
				"partition-test-region": "partition-test-region-3",
			},
			Annotations: map[string]string{
				"experimental.core.kcp.io/unschedulable": "true",
			},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL: "https://base.kcp.test.dev",
		},
	}
	shard3, err = shardClient.Cluster(core.RootCluster.Path()).Create(ctx, shard3, metav1.CreateOptions{})
	require.NoError(t, err, "error creating shard")
	defer func() {
		err = shardClient.Cluster(core.RootCluster.Path()).Delete(ctx, shard3.Name, metav1.DeleteOptions{})
		require.NoError(t, err, "error deleting shard")
	}()
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) && partitionSet.Status.Count == uint16(2) {
			return true, ""
		}
		return false, fmt.Sprintf("expected 2 partitions, but got %d", partitionSet.Status.Count)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected the partition count to become 2")
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 2 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 2 partitions, but got %d, details: %s", len(partitions.Items), spew.Sdump(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 2 partitions")

	t.Logf("Excluding the shard of the third region")
	shard3.Labels = map[string]string{
		"partition-test-region": "partition-test-region-3",
		"excluded":              "true",
	}
	_, err = shardClient.Cluster(core.RootCluster.Path()).Update(ctx, shard3, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating shard")
	framework.Eventually(t, func() (bool, string) {
		partitionSet, err = partitionSetClient.Cluster(partitionClusterPath).Get(ctx, partitionSet.Name, metav1.GetOptions{})
		require.NoError(t, err, "error retrieving partitionSet")
		if conditions.IsTrue(partitionSet, topologyv1alpha1.PartitionsReady) && partitionSet.Status.Count == uint16(1) {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d", partitionSet.Status.Count)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected the partition count to become 1")
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d, details: %s", len(partitions.Items), spew.Sdump(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 1 partition")
}

func TestPartitionSetAdmission(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := framework.SharedKcpServer(t)

	// Create organization and workspace.
	// Organizations help with multiple runs.
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	partitionClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("partitionset-admission"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	partitionSetClient := kcpClusterClient.TopologyV1alpha1().PartitionSets()
	partitionClient := kcpClusterClient.TopologyV1alpha1().Partitions()
	shardClient := kcpClusterClient.CoreV1alpha1().Shards()

	errorPartitionSet := &topologyv1alpha1.PartitionSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "admission-partitionset",
		},
		Spec: topologyv1alpha1.PartitionSetSpec{
			Dimensions: []string{"region"},
		},
	}

	t.Logf("Key too long in matchExpressions")
	errorPartitionSet.Spec.ShardSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "region/1234567890123456789012345678901234567890123456789012345678901234567890",
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"antartica", "greenland"},
			},
		},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, errorPartitionSet, metav1.CreateOptions{})
	require.Error(t, err, "error creating partitionSet expected")

	t.Logf("Character not allowed at first place in matchExpressions values")
	errorPartitionSet.Spec.ShardSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "region",
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"antartica", "_A.123456789012345678901234567890123456789012345678901234567890"},
			},
		},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, errorPartitionSet, metav1.CreateOptions{})
	require.Error(t, err, "error creating partitionSet expected")

	t.Logf("Invalid value in matchExpressions operator")
	errorPartitionSet.Spec.ShardSelector = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "region",
				Operator: "DoesNotExist",
				Values:   []string{"antartica", "greenland"},
			},
		},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, errorPartitionSet, metav1.CreateOptions{})
	require.Error(t, err, "error creating partitionSet expected")

	t.Logf("Invalid key in matchLabels")
	errorPartitionSet.Spec.ShardSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"1234567890123456789_01234567890123456789/aaa": "keynotvalid"},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, errorPartitionSet, metav1.CreateOptions{})
	require.Error(t, err, "error creating partitionSet expected")

	t.Logf("Invalid value in matchLabels")
	errorPartitionSet.Spec.ShardSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"valuenotvalid": "1234567890123456789%%%01234567890123456789"},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, errorPartitionSet, metav1.CreateOptions{})
	require.Error(t, err, "error creating partitionSet expected")

	t.Logf("Partition name cut when the label values sum up")
	partitionSet := &topologyv1alpha1.PartitionSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-partitionset",
		},
		Spec: topologyv1alpha1.PartitionSetSpec{
			Dimensions: []string{"partition-test-label1", "partition-test-label2", "partition-test-label3", "partition-test-label4", "partition-test-label5"},
		},
	}
	_, err = partitionSetClient.Cluster(partitionClusterPath).Create(ctx, partitionSet, metav1.CreateOptions{})
	require.NoError(t, err, "error updating partitionSet")
	labelValues := []string{
		"label1-12345678901234567890123456789012345678901234567890",
		"label2-12345678901234567890123456789012345678901234567890",
		"label3-12345678901234567890123456789012345678901234567890",
		"label4-12345678901234567890123456789012345678901234567890",
		"label5-12345678901234567890123456789012345678901234567890",
	}
	admissionShard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partition-shard-adm",
			Labels: map[string]string{
				"partition-test-label1": labelValues[0],
				"partition-test-label2": labelValues[1],
				"partition-test-label3": labelValues[2],
				"partition-test-label4": labelValues[3],
				"partition-test-label5": labelValues[4],
			},
			Annotations: map[string]string{
				"experimental.core.kcp.io/unschedulable": "true",
			},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL: "https://base.kcp.test.dev",
		},
	}
	shard, err := shardClient.Cluster(core.RootCluster.Path()).Create(ctx, admissionShard, metav1.CreateOptions{})
	require.NoError(t, err, "error creating shard")
	defer func() {
		err = shardClient.Cluster(core.RootCluster.Path()).Delete(ctx, shard.Name, metav1.DeleteOptions{})
		require.NoError(t, err, "error deleting shard")
	}()
	var partitions *topologyv1alpha1.PartitionList
	framework.Eventually(t, func() (bool, string) {
		partitions, err = partitionClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error retrieving partitions")
		if len(partitions.Items) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 partition, but got %d", len(partitions.Items))
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected 1 partition")
	expectedName := partitionSet.Name + "-" + strings.Join(labelValues, "-")
	expectedName = expectedName[:validation.DNS1123LabelMaxLength-5]
	require.EqualValues(t, expectedName, partitions.Items[0].Name[:len(partitions.Items[0].Name)-5],
		"partition name not as expected, got: %s", partitions.Items[0].Name)
}
