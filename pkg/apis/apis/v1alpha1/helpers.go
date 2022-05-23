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

package v1alpha1

import (
	"github.com/kcp-dev/logicalcluster"
)

// GetLogicalCluster returns the referenced logical cluster relative to the given APIBinding logical
// cluster. It returns an empty logical cluster if none of the mutual-exclusive fields is set.
// Validation makes sure that is not the case for persisted APIBindings.
func (r *WorkspaceExportReference) GetLogicalCluster(base logicalcluster.Name) (logicalcluster.Name, bool) {
	if r.LogicalCluster != "" {
		return logicalcluster.New(r.LogicalCluster), true
	}
	if r.WorkspaceName != "" {
		parent, hasParent := base.Parent()
		if !hasParent {
			return logicalcluster.Name{}, false
		}
		return parent.Join(r.WorkspaceName), true
	}
	return logicalcluster.Name{}, false
}
