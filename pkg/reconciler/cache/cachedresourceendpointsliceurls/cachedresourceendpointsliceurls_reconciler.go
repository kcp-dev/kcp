/*
Copyright 2025 The kcp Authors.

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

package cachedresourceendpointsliceurls

import (
	"context"
	"net/url"
	"path"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	cachev1alpha1apply "github.com/kcp-dev/sdk/client/applyconfiguration/cache/v1alpha1"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	replicationvw "github.com/kcp-dev/kcp/pkg/virtual/replication"
)

type result struct {
	url    string
	remove bool
}

func (c *controller) reconcile(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	return endpointsReconciler{
		getAPIExport:                     c.getAPIExport,
		listAPIBindingsByAPIExport:       c.listAPIBindingsByAPIExport,
		getMyShard:                       c.getMyShard,
		patchCachedResourceEndpointSlice: c.patchCachedResourceEndpointSlice,
	}.reconcile(ctx, slice)
}

type endpointsReconciler struct {
	getAPIExport                     func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	listAPIBindingsByAPIExport       func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error)
	getMyShard                       func() (*corev1alpha1.Shard, error)
	patchCachedResourceEndpointSlice func(ctx context.Context, cluster logicalcluster.Path, patch *cachev1alpha1apply.CachedResourceEndpointSliceApplyConfiguration) error
}

func (r endpointsReconciler) reconcile(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	for _, condition := range slice.Status.Conditions {
		if !conditions.IsTrue(slice, condition.Type) {
			return false, nil
		}
	}

	rs, err := r.updateEndpoints(ctx, slice)
	if err != nil {
		return true, err
	}
	if rs == nil {
		// No change, nothing to do.
		return false, nil
	}

	// Patch the object
	patch := cachev1alpha1apply.CachedResourceEndpointSlice(slice.Name)
	if rs.remove {
		patch.WithStatus(cachev1alpha1apply.CachedResourceEndpointSliceStatus())
	} else {
		patch.WithStatus(cachev1alpha1apply.CachedResourceEndpointSliceStatus().
			WithCachedResourceEndpoints(cachev1alpha1apply.CachedResourceEndpoint().WithURL(rs.url)))
	}
	cluster := logicalcluster.From(slice)
	err = r.patchCachedResourceEndpointSlice(ctx, cluster.Path(), patch)
	if err != nil {
		return true, err
	}
	return false, nil
}

func (r *endpointsReconciler) updateEndpoints(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (*result, error) {
	logger := klog.FromContext(ctx)

	thisShard, err := r.getMyShard()
	if err != nil {
		return nil, err
	}

	if thisShard.Spec.VirtualWorkspaceURL == "" {
		// We don't have VW URLs, bail out.
		return nil, nil
	}

	exportPath := logicalcluster.NewPath(slice.Spec.APIExport.Path)
	if exportPath.Empty() {
		exportPath = logicalcluster.From(slice).Path()
	}

	export, err := r.getAPIExport(exportPath, slice.Spec.APIExport.Name)
	if err != nil {
		return nil, err
	}

	bindings, err := r.listAPIBindingsByAPIExport(export)
	if err != nil {
		return nil, err
	}

	if len(bindings) == 0 {
		// We don't have any consumers, so clean up all endpoints.
		return &result{remove: true}, nil
	}

	shardSelector, err := labels.Parse(slice.Status.ShardSelector)
	if err != nil {
		return nil, err
	}
	if !shardSelector.Matches(labels.Set(thisShard.Labels)) {
		// We don't belong in the partition, so do nothing.
		return nil, nil
	}

	baseURL, err := url.Parse(thisShard.Spec.VirtualWorkspaceURL)
	if err != nil {
		logger = logging.WithObject(logger, thisShard)
		logger.Error(err, "error parsing shard.spec.virtualWorkspaceURL",
			"VirtualWorkspaceURL", thisShard.Spec.VirtualWorkspaceURL)
		return nil, nil
	}

	// URL format: <Shard URL>/services/replication/<CachedResourceEndpointSlice cluster>/<CachedResourceEndpointSlice name>
	baseURL.Path = path.Join(
		baseURL.Path,
		virtualworkspacesoptions.DefaultRootPathPrefix,
		replicationvw.VirtualWorkspaceName,
		logicalcluster.From(slice).String(),
		slice.Name,
	)
	newURL := baseURL.String()

	for _, e := range slice.Status.CachedResourceEndpoints {
		if e.URL == newURL {
			return nil, nil
		}
	}

	return &result{url: newURL}, nil
}
