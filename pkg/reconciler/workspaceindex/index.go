/*
Copyright 2021 The KCP Authors.

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

package workspaceindex

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"

	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// NewIndex creates a new workspace to shard assignment index.
func NewIndex() *index {
	return &index{
		workspaceMapping: map[string]map[string][]ShardAssignment{},
	}
}

// ShardAssignment holds a range of resource versions for which a workspace
// was scheduled to a particular shard. math.MaxInt64 is used to indicate an
// open upper interval.
// TODO: should we just store ints on the API object?
type ShardAssignment struct {
	Name                      string `json:"name"`
	LiveBeforeResourceVersion int64  `json:"liveBeforeResourceVersion"`
	LiveAfterResourceVersion  int64  `json:"liveAfterResourceVersion"`
}

type Index interface {
	Record(workspace *tenancyv1alpha1.ClusterWorkspace) error
	Get(organization, workspace string) ([]ShardAssignment, error)
	json.Marshaler
}

type index struct {
	sync.RWMutex
	// workspaceMapping holds resourceVersion-bound locations for workspaces
	// through history. The list of locations is sorted. The mapping is from
	// org to workspace to history.
	workspaceMapping map[string]map[string][]ShardAssignment
}

func (i *index) Record(workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if !conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled) {
		klog.Infof("workspace %s/%s not scheduled, skipping...", workspace.ClusterName, workspace.Name)
		return nil
	}
	history, err := convertHistory(workspace.Status.Location.History)
	if err != nil {
		return fmt.Errorf("invalid history for workspace %s/%s: %w", workspace.ClusterName, workspace.Name, err)
	}
	// TODO: how to handle the root itself?
	org := workspace.ClusterName
	if workspace.ClusterName != helper.RootCluster {
		orgName, workspaceName, err := helper.ParseLogicalClusterName(workspace.ClusterName)
		if err != nil {
			klog.Errorf("failed to determine org for cluster: %v", err)
			return nil
		}
		if orgName != helper.RootCluster {
			klog.Errorf("invalid org %q, only one level of nesting allowed", orgName)
			return nil
		}
		org = workspaceName
	}

	i.Lock()
	if _, ok := i.workspaceMapping[org]; !ok {
		i.workspaceMapping[org] = map[string][]ShardAssignment{}
	}
	i.workspaceMapping[org][workspace.Name] = history
	klog.Infof("added history for %s->%s:%v", org, workspace.Name, history)
	i.Unlock()
	return nil
}

func convertHistory(original []tenancyv1alpha1.ShardStatus) ([]ShardAssignment, error) {
	var history []ShardAssignment
	for i := range original {
		converted, err := convertShardStatus(fmt.Sprintf("history[%d]", i), original[i])
		if err != nil {
			return nil, err
		}
		history = append(history, converted)
	}
	return history, nil
}

func convertShardStatus(prefix string, original tenancyv1alpha1.ShardStatus) (ShardAssignment, error) {
	var liveBeforeResourceVersion int64
	if original.LiveBeforeResourceVersion == "" {
		liveBeforeResourceVersion = math.MaxInt64
	} else {
		var err error
		liveBeforeResourceVersion, err = strconv.ParseInt(original.LiveBeforeResourceVersion, 10, 64)
		if err != nil {
			return ShardAssignment{}, fmt.Errorf("%s.liveBeforeResourceVersion: invalid: %w", prefix, err)
		}
	}
	liveAfterResourceVersion, err := strconv.ParseInt(original.LiveAfterResourceVersion, 10, 64)
	if err != nil {
		return ShardAssignment{}, fmt.Errorf("%s.liveAfterResourceVersion: invalid: %w", prefix, err)
	}
	return ShardAssignment{
		Name:                      original.Name,
		LiveBeforeResourceVersion: liveBeforeResourceVersion,
		LiveAfterResourceVersion:  liveAfterResourceVersion,
	}, nil
}

func (i *index) Get(organization, workspace string) ([]ShardAssignment, error) {
	i.RLock()
	defer i.RUnlock()
	historyByWorkspace, organizationExists := i.workspaceMapping[organization]
	if !organizationExists {
		return nil, fmt.Errorf("organization %q not found", organization)
	}
	history, workspaceExists := historyByWorkspace[workspace]
	if !workspaceExists {
		return nil, fmt.Errorf("workspace %q not found", workspace)
	}
	return history, nil
}

func (i *index) MarshalJSON() ([]byte, error) {
	i.RLock()
	defer i.RUnlock()
	return json.Marshal(i.workspaceMapping)
}

var _ json.Marshaler = &index{}
