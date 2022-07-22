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

package virtualworkspaceurls

import (
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	syncerbuilder "github.com/kcp-dev/kcp/pkg/virtual/syncer/builder"
)

func (c *Controller) reconcile(syncTarget *workloadv1alpha1.SyncTarget, workspaceShards []*v1alpha1.ClusterWorkspaceShard) (*workloadv1alpha1.SyncTarget, error) {
	syncTargetCopy := syncTarget.DeepCopy()

	desiredURLs := sets.NewString()
	for _, workspaceShard := range workspaceShards {
		if workspaceShard.Spec.ExternalURL != "" {
			syncerVirtualWorkspaceURL, err := url.Parse(workspaceShard.Spec.ExternalURL)
			if err != nil {
				klog.Errorf("failed to parse workspaceShard.Spec.ExternalURL: %v", err)
				return nil, err
			}
			syncerVirtualWorkspaceURL.Path = path.Join(
				syncerVirtualWorkspaceURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.SyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
			)
			desiredURLs.Insert(syncerVirtualWorkspaceURL.String())
		}
	}

	syncTargetCopy.Status.VirtualWorkspaces = nil
	for _, url := range desiredURLs.List() {
		syncTargetCopy.Status.VirtualWorkspaces = append(
			syncTargetCopy.Status.VirtualWorkspaces,
			workloadv1alpha1.VirtualWorkspace{
				URL: url,
			})
	}
	return syncTargetCopy, nil
}
