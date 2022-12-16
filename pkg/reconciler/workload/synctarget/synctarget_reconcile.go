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

package synctarget

import (
	"context"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	syncerbuilder "github.com/kcp-dev/kcp/pkg/virtual/syncer/builder"
)

func (c *Controller) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget, workspaceShards []*corev1alpha1.Shard) (*workloadv1alpha1.SyncTarget, error) {
	logger := klog.FromContext(ctx)
	syncTargetCopy := syncTarget.DeepCopy()

	labels := syncTargetCopy.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[workloadv1alpha1.InternalSyncTargetKeyLabel] = workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTargetCopy), syncTargetCopy.Name)
	syncTargetCopy.SetLabels(labels)

	desiredURLs := sets.NewString()
	for _, workspaceShard := range workspaceShards {
		if workspaceShard.Spec.ExternalURL != "" {
			syncerVirtualWorkspaceURL, err := url.Parse(workspaceShard.Spec.ExternalURL)
			if err != nil {
				logger.Error(err, "failed to parse workspaceShard.Spec.ExternalURL")
				return nil, err
			}
			syncerVirtualWorkspaceURL.Path = path.Join(
				syncerVirtualWorkspaceURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.SyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
				string(syncTargetCopy.UID),
			)
			desiredURLs.Insert(syncerVirtualWorkspaceURL.String())
		}
	}

	if syncTargetCopy.Status.VirtualWorkspaces != nil {
		currentURLs := sets.NewString()
		for _, virtualWorkspace := range syncTargetCopy.Status.VirtualWorkspaces {
			currentURLs = currentURLs.Insert(virtualWorkspace.URL)
		}

		if desiredURLs.Equal(currentURLs) {
			return syncTargetCopy, nil
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
