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

package location

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// LocationWorkloadClusters returns a list of workload clusters that match the given location definition.
func LocationWorkloadClusters(workloadClusters []*workloadv1alpha1.WorkloadCluster, location *schedulingv1alpha1.Location) (ret []*workloadv1alpha1.WorkloadCluster, err error) {
	sel, err := metav1.LabelSelectorAsSelector(location.Spec.InstanceSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label selector %v in location %s: %w", location.Spec.InstanceSelector, location.Name, err)
	}

	// find location clusters
	for _, wc := range workloadClusters {
		if sel.Matches(labels.Set(wc.Labels)) {
			ret = append(ret, wc)
		}
	}

	return ret, nil
}

// FilterReady returns the ready workload clusters.
func FilterReady(workloadClusters []*workloadv1alpha1.WorkloadCluster) []*workloadv1alpha1.WorkloadCluster {
	var ready []*workloadv1alpha1.WorkloadCluster
	for _, wc := range workloadClusters {
		if conditions.IsTrue(wc, conditionsapi.ReadyCondition) && !wc.Spec.Unschedulable {
			ready = append(ready, wc)
		}
	}
	return ready
}
