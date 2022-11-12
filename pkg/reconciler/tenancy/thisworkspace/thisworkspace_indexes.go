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

package thisworkspace

import (
	"crypto/sha256"
	"strings"

	"github.com/martinlindhe/base36"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

const (
	byCurrentShard     = "byCurrentShard"
	byBase36Sha224Name = "byBase36Sha224Name"
	unschedulable      = "unschedulable"
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

func indexByBase36Sha224Name(obj interface{}) ([]string, error) {
	s := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
	return []string{ByBase36Sha224NameValue(s.Name)}, nil
}

func ByBase36Sha224NameValue(name string) string {
	hash := sha256.Sum224([]byte(name))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))

	return base36hash[:8]
}
