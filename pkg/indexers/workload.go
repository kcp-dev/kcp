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

package indexers

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	SyncTargetsBySyncTargetKey            = "SyncTargetsBySyncTargetKey"
	PlacementBySpecifiedLocationWorkspace = "PlacementBySpecifiedLocationWorkspace"
	PlacementBySelectedLocationWorkspace  = "PlacementBySelectedLocationWorkspace"
)

func IndexSyncTargetsBySyncTargetKey(obj interface{}) ([]string, error) {
	syncTarget, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a workloadv1alpha1.SyncTarget, but is %T", obj)
	}

	return []string{workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.Name)}, nil
}

func IndexPlacementBySpecifiedLocationWorkspace(obj interface{}) ([]string, error) {
	placement, ok := obj.(*schedulingv1alpha1.Placement)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a Placement, but is %T", obj)
	}

	if len(placement.Spec.LocationWorkspace) == 0 {
		return []string{logicalcluster.From(placement).String()}, nil
	}

	return []string{placement.Spec.LocationWorkspace}, nil
}

func IndexPlacementBySelectedLocationWorkspace(obj interface{}) ([]string, error) {
	placement, ok := obj.(*schedulingv1alpha1.Placement)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a Placement, but is %T", obj)
	}

	if placement.Status.SelectedLocation == nil {
		return []string{}, nil
	}

	return []string{placement.Status.SelectedLocation.Path}, nil
}
