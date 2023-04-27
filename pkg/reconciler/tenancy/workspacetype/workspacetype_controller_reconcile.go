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

package workspacetype

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

func (c *controller) reconcile(ctx context.Context, wt *tenancyv1alpha1.WorkspaceType) {
	if err := c.updateVirtualWorkspaceURLs(ctx, wt); err != nil {
		conditions.MarkFalse(
			wt,
			tenancyv1alpha1.WorkspaceTypeVirtualWorkspaceURLsReady,
			tenancyv1alpha1.ErrorGeneratingURLsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
	} else {
		conditions.MarkTrue(
			wt,
			tenancyv1alpha1.WorkspaceTypeVirtualWorkspaceURLsReady,
		)
	}

	conditions.SetSummary(wt)
}

func (c *controller) updateVirtualWorkspaceURLs(ctx context.Context, wt *tenancyv1alpha1.WorkspaceType) error {
	logger := klog.FromContext(ctx)
	shards, err := c.listShards()
	if err != nil {
		return fmt.Errorf("error listing Shards: %w", err)
	}

	desiredURLs := sets.New[string]()
	for _, shard := range shards {
		if shard.Spec.VirtualWorkspaceURL == "" {
			continue
		}

		u, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
		if err != nil {
			// Should never happen
			logger.Error(err, "error parsing shard.spec.virtualWorkspaceURL", "virtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL)
			continue
		}

		u.Path = path.Join(
			u.Path,
			virtualworkspacesoptions.DefaultRootPathPrefix,
			initializingworkspaces.VirtualWorkspaceName,
			string(initialization.InitializerForType(wt)),
		)

		desiredURLs.Insert(u.String())
	}

	wt.Status.VirtualWorkspaces = nil

	for _, u := range sets.List[string](desiredURLs) {
		wt.Status.VirtualWorkspaces = append(wt.Status.VirtualWorkspaces, tenancyv1alpha1.VirtualWorkspace{
			URL: u,
		})
	}

	return nil
}
