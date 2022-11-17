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

package clusterworkspace

import (
	"fmt"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

const (
	byCurrentShard = "byCurrentShard"
	unschedulable  = "unschedulable"
	byPhase        = "byPhase"
)

func indexByCurrentShard(obj interface{}) ([]string, error) {
	ws := obj.(*tenancyv1alpha1.ClusterWorkspace)
	return []string{ws.Status.Location.Current}, nil
}

func indexUnschedulable(obj interface{}) ([]string, error) {
	workspace := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && conditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable {
		return []string{"true"}, nil
	}
	return []string{}, nil
}

func indexByPhase(obj interface{}) ([]string, error) {
	workspace, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a tenancyv1alpha1.ClusterWorkspace, but is %T", obj)
	}

	return []string{string(workspace.Status.Phase)}, nil
}
