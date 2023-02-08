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

	topologyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/topology/v1alpha1"
)

// generatePartition generates the Partition specifications based on
// the provided matchExpressions and matchLabels.
func generatePartition(name string, matchExpressions []metav1.LabelSelectorRequirement, matchLabels map[string]string, dimensions []string) *topologyv1alpha1.Partition {
	pname := name
	labels := make([]string, len(dimensions))
	copy(labels, dimensions)
	sort.Strings(labels)
	for _, label := range labels {
		pname = pname + "-" + strings.ToLower(matchLabels[label])
	}

	return &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pname + "-",
		},
		Spec: topologyv1alpha1.PartitionSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels:      matchLabels,
				MatchExpressions: matchExpressions,
			},
		},
	}
}
