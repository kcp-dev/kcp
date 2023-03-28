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
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

// generatePartition generates the Partition specifications based on
// the provided matchExpressions and matchLabels.
func generatePartition(name string, matchExpressions []metav1.LabelSelectorRequirement, matchLabels map[string]string, dimensions []string) *topologyv1alpha1.Partition {
	name = generatePartitionName(name, matchLabels, dimensions)
	return &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name + "-",
		},
		Spec: topologyv1alpha1.PartitionSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels:      matchLabels,
				MatchExpressions: matchExpressions,
			},
		},
	}
}

// generatePartitionName creates a name based on the dimension values.
func generatePartitionName(name string, matchLabels map[string]string, dimensions []string) string {
	labels := make([]string, len(dimensions))
	copy(labels, dimensions)
	sort.Strings(labels)
	for _, label := range labels {
		name = name + "-" + strings.ToLower(matchLabels[label])
	}
	name = name[:min(validation.DNS1123SubdomainMaxLength-1, len(name))]
	return name
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
