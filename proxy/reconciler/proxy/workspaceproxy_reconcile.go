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

package workspaceproxy

import (
	"context"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxybuilder "github.com/kcp-dev/kcp/proxy/virtual/proxy/builder"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func (c *Controller) reconcile(ctx context.Context, syncTarget *proxyv1alpha1.WorkspaceProxy, workspaceShards []*corev1alpha1.Shard) (*proxyv1alpha1.WorkspaceProxy, error) {
	logger := klog.FromContext(ctx)
	proxyTargetCopy := syncTarget.DeepCopy()

	labels := proxyTargetCopy.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[proxyv1alpha1.InternalWorkspaceProxyKeyLabel] = proxyv1alpha1.ToProxyTargetKey(logicalcluster.From(proxyTargetCopy), proxyTargetCopy.Name)
	proxyTargetCopy.SetLabels(labels)

	desiredVWURLs := map[string]proxyv1alpha1.VirtualWorkspace{}
	desiredTunnelWorkspaceURLs := map[string]proxyv1alpha1.TunnelWorkspace{}
	syncTargetClusterName := logicalcluster.From(syncTarget)

	var rootShardKey string
	for _, workspaceShard := range workspaceShards {
		if workspaceShard.Spec.VirtualWorkspaceURL != "" {
			shardVWURL, err := url.Parse(workspaceShard.Spec.VirtualWorkspaceURL)
			if err != nil {
				logger.Error(err, "failed to parse workspaceShard.Spec.VirtualWorkspaceURL")
				return nil, err
			}

			syncerVirtualWorkspaceURL := *shardVWURL
			syncerVirtualWorkspaceURL.Path = path.Join(
				shardVWURL.Path,
				virtualworkspacesoptions.DefaultRootPathPrefix,
				proxybuilder.ProxyVirtualWorkspaceName,
				logicalcluster.From(proxyTargetCopy).String(),
				proxyTargetCopy.Name,
				string(proxyTargetCopy.UID),
			)

			syncerURL := (&syncerVirtualWorkspaceURL).String()

			if workspaceShard.Name == corev1alpha1.RootShard {
				rootShardKey = shardVWURL.String()
			}
			desiredVWURLs[shardVWURL.String()] = proxyv1alpha1.VirtualWorkspace{
				SyncerURL: syncerURL,
			}

			tunnelWorkspaceURL, err := url.JoinPath(workspaceShard.Spec.BaseURL, syncTargetClusterName.Path().RequestPath())
			if err != nil {
				return nil, err
			}
			desiredTunnelWorkspaceURLs[shardVWURL.String()] = proxyv1alpha1.TunnelWorkspace{
				URL: tunnelWorkspaceURL,
			}
		}
	}

	// Let's always add the desired URLs in the same order:
	// - urls for the root shard will always be added at the first place,
	//   in order to ensure compatibility with the shard-unaware Syncer
	// - urls for other shards which will be added in the lexical order of the
	// corresponding shard URLs.
	var desiredVirtualWorkspaces []proxyv1alpha1.VirtualWorkspace //nolint:prealloc
	if rootShardVirtualWorkspace, ok := desiredVWURLs[rootShardKey]; ok {
		desiredVirtualWorkspaces = append(desiredVirtualWorkspaces, rootShardVirtualWorkspace)
		delete(desiredVWURLs, rootShardKey)
	}
	for _, shardURL := range sets.StringKeySet(desiredVWURLs).List() {
		desiredVirtualWorkspaces = append(desiredVirtualWorkspaces, desiredVWURLs[shardURL])
	}
	var desiredTunnelWorkspaces []proxyv1alpha1.TunnelWorkspace //nolint:prealloc
	if rootShardTunnelWorkspace, ok := desiredTunnelWorkspaceURLs[rootShardKey]; ok {
		desiredTunnelWorkspaces = append(desiredTunnelWorkspaces, rootShardTunnelWorkspace)
		delete(desiredTunnelWorkspaceURLs, rootShardKey)
	}
	for _, shardURL := range sets.StringKeySet(desiredTunnelWorkspaceURLs).List() {
		desiredTunnelWorkspaces = append(desiredTunnelWorkspaces, desiredTunnelWorkspaceURLs[shardURL])
	}

	proxyTargetCopy.Status.VirtualWorkspaces = desiredVirtualWorkspaces
	proxyTargetCopy.Status.TunnelWorkspaces = desiredTunnelWorkspaces
	return proxyTargetCopy, nil
}
