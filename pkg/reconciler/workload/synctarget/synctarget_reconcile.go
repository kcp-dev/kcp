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

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	syncerbuilder "github.com/kcp-dev/kcp/tmc/pkg/virtual/syncer/builder"
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

	desiredURLs := map[string]workloadv1alpha1.VirtualWorkspace{}

	for _, workspaceShard := range workspaceShards {
		if workspaceShard.Spec.ExternalURL != "" {
			sharedExternalURL, err := url.Parse(workspaceShard.Spec.ExternalURL)
			if err != nil {
				logger.Error(err, "failed to parse workspaceShard.Spec.ExternalURL")
				return nil, err
			}

			syncerVirtualWorkspaceURL := *sharedExternalURL
			syncerVirtualWorkspaceURL.Path = path.Join(
				sharedExternalURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.SyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
				string(syncTargetCopy.UID),
			)

			upsyncerVirtualWorkspaceURL := *sharedExternalURL
			(&upsyncerVirtualWorkspaceURL).Path = path.Join(
				sharedExternalURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				syncerbuilder.UpsyncerVirtualWorkspaceName,
				logicalcluster.From(syncTargetCopy).String(),
				syncTargetCopy.Name,
				string(syncTargetCopy.UID),
			)

			syncerURL := (&syncerVirtualWorkspaceURL).String()
			upsyncerURL := (&upsyncerVirtualWorkspaceURL).String()

			desiredURLs[sharedExternalURL.String()] = workloadv1alpha1.VirtualWorkspace{
				SyncerURL:   syncerURL,
				UpsyncerURL: upsyncerURL,
			}
		}
	}

	// Let's always add the desired URL in the same order, which will be the order of the
	// corresponding shard URLs
	var desiredVirtualWorkspaces []workloadv1alpha1.VirtualWorkspace //nolint:prealloc
	for _, shardURL := range sets.StringKeySet(desiredURLs).List() {
		desiredVirtualWorkspaces = append(desiredVirtualWorkspaces, desiredURLs[shardURL])
	}

	if syncTargetCopy.Status.VirtualWorkspaces != nil {
		if equality.Semantic.DeepEqual(syncTargetCopy.Status.VirtualWorkspaces, desiredVirtualWorkspaces) {
			return syncTargetCopy, nil
		}
	}

	syncTargetCopy.Status.VirtualWorkspaces = desiredVirtualWorkspaces
	return syncTargetCopy, nil
}
