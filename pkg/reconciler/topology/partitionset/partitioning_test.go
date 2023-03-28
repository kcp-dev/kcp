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
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestPartition(t *testing.T) {
	shards := []*corev1alpha1.Shard{
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
				Labels: map[string]string{
					"region":      "Europe",
					"cloud":       "Azure",
					"az":          "EU-1",
					"environment": "prod",
				},
				Name: "shard2",
			},
			Spec: corev1alpha1.ShardSpec{
				VirtualWorkspaceURL: "https://server-1.kcp.dev/",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
				Labels: map[string]string{
					"region":      "Europe",
					"cloud":       "Azure",
					"az":          "EU-2",
					"environment": "prod",
				},
				Name: "shard4",
			},
			Spec: corev1alpha1.ShardSpec{
				VirtualWorkspaceURL: "https://server-2.kcp.dev/",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
				Labels: map[string]string{
					"region":      "Europe",
					"cloud":       "Azure",
					"az":          "EU-3",
					"environment": "prod",
				},
				Name: "shard5",
			},
			Spec: corev1alpha1.ShardSpec{
				VirtualWorkspaceURL: "https://server-3.kcp.dev/",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
				Labels: map[string]string{
					"region":      "Europe",
					"cloud":       "AWS",
					"az":          "EU-3",
					"environment": "prod",
				},
				Name: "shard5",
			},
			Spec: corev1alpha1.ShardSpec{
				VirtualWorkspaceURL: "https://server-4.kcp.dev/",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root:org:ws",
				},
				Labels: map[string]string{
					"region":      "Asia",
					"cloud":       "Azure",
					"az":          "cn-west-1",
					"environment": "prod",
				},
				Name: "shard7",
			},
			Spec: corev1alpha1.ShardSpec{
				VirtualWorkspaceURL: "https://server-5.kcp.dev/",
			},
		},
	}

	matchLabelsMap := partition(shards, []string{}, nil)
	require.Equal(t, 0, len(matchLabelsMap), "No label selector expected when no dimension is provided, got: %v", matchLabelsMap)

	matchLabelsMap = partition(shards, []string{"doesnotexist"}, nil)
	require.Equal(t, 0, len(matchLabelsMap), "No label selector expected when no shard with the dimension, got: %v", matchLabelsMap)

	matchLabelsMap = partition(shards, []string{"region"}, nil)
	require.Equal(t, 2, len(matchLabelsMap), "2 label selectors for region: Europe and Asia expected, got: %v", matchLabelsMap)

	matchLabelsMap = partition(shards, []string{"region", "cloud"}, nil)
	require.Equal(t, 3, len(matchLabelsMap), "3 label selectors for: Asia/Azure, Europe/AWS and Europe/Azure expected, got: %v", matchLabelsMap)

	matchLabelsMap = partition(shards, []string{"region", "cloud"}, map[string]string{"environment": "prod"})
	require.Equal(t, 3, len(matchLabelsMap), "3 label selectors for: Asia/Azure, Europe/AWS, Europe/Azure expected, got: %v", matchLabelsMap)
	for _, v := range matchLabelsMap {
		require.Equal(t, "prod", v["environment"], "Expected that all partitions have a label selector for environment = prod")
	}
}

func TestGeneratePartitionName(t *testing.T) {
	name := generatePartitionName(
		"partitionset",
		map[string]string{"region": "europe", "cloud": "EKS", "verylong": "123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890"},
		[]string{"region", "verylong"},
	)
	require.Equal(t, "partitionset-europe-123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890"[:validation.DNS1123SubdomainMaxLength-1], name)
}
