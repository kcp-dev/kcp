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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// LocationSyncTargets returns a list of sync targets that match the given location definition.
func LocationSyncTargets(syncTargets []*workloadv1alpha1.SyncTarget, location *schedulingv1alpha1.Location) (ret []*workloadv1alpha1.SyncTarget, err error) {
	sel, err := metav1.LabelSelectorAsSelector(location.Spec.InstanceSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label selector %v in location %s: %w", location.Spec.InstanceSelector, location.Name, err)
	}

	// find location clusters
	for _, wc := range syncTargets {
		if sel.Matches(labels.Set(wc.Labels)) {
			ret = append(ret, wc)
		}
	}

	return ret, nil
}

// FilterReady returns the ready sync targets.
func FilterReady(syncTargets []*workloadv1alpha1.SyncTarget) []*workloadv1alpha1.SyncTarget {
	ready := make([]*workloadv1alpha1.SyncTarget, 0, len(syncTargets))
	for _, wc := range syncTargets {
		if conditions.IsTrue(wc, conditionsv1alpha1.ReadyCondition) && !wc.Spec.Unschedulable {
			ready = append(ready, wc)
		}
	}
	return ready
}

// FilterNonEvicting filters out the evicting sync targets.
func FilterNonEvicting(syncTargets []*workloadv1alpha1.SyncTarget) []*workloadv1alpha1.SyncTarget {
	ret := make([]*workloadv1alpha1.SyncTarget, 0, len(syncTargets))
	now := time.Now()
	for _, wc := range syncTargets {
		if wc.Spec.EvictAfter == nil || now.Before(wc.Spec.EvictAfter.Time) {
			ret = append(ret, wc)
		}
	}
	return ret
}
