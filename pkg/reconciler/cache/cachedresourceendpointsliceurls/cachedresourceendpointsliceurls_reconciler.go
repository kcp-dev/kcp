/*
Copyright 2025 The KCP Authors.

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
	"fmt"
	"net/url"
	"path"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	replicationvw "github.com/kcp-dev/kcp/pkg/virtual/replication"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	cachev1alpha1apply "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/cache/v1alpha1"
)

type result struct {
	url    string
	remove bool
}

func (c *controller) reconcile(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	return endpointsReconciler{
		listAPIExportsByCachedResourceEndpointSlice: c.listAPIExportsByCachedResourceEndpointSlice,
		listAPIBindingsByAPIExports:                 c.listAPIBindingsByAPIExports,
		getMyShard:                                  c.getMyShard,
		getCachedResource:                           c.getCachedResource,
		patchAPIExportEndpointSlice:                 c.patchAPIExportEndpointSlice,
	}.reconcile(ctx, slice)
}

type endpointsReconciler struct {
	listAPIExportsByCachedResourceEndpointSlice func(slice *cachev1alpha1.CachedResourceEndpointSlice) ([]*apisv1alpha2.APIExport, error)
	listAPIBindingsByAPIExports                 func(exports []*apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error)
	getMyShard                                  func() (*corev1alpha1.Shard, error)
	getCachedResource                           func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error)
	patchAPIExportEndpointSlice                 func(ctx context.Context, cluster logicalcluster.Path, patch *cachev1alpha1apply.CachedResourceEndpointSliceApplyConfiguration) error
}

func (r endpointsReconciler) reconcile(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	for _, condition := range slice.Status.Conditions {
		if !conditions.IsTrue(slice, condition.Type) {
			return false, nil
		}
	}

	fmt.Println("### endpointsReconciler.reconcile 1")

	rs, err := r.updateEndpoints(ctx, slice)
	if err != nil {
		fmt.Println("### endpointsReconciler.reconcile 2")
		return true, err
	}
	if rs == nil {
		// No change, nothing to do.
		fmt.Println("### endpointsReconciler.reconcile 3")
		return false, nil
	}

	fmt.Println("### endpointsReconciler.reconcile 4")
	// Patch the object
	patch := cachev1alpha1apply.CachedResourceEndpointSlice(slice.Name)
	if rs.remove {
		fmt.Println("### endpointsReconciler.reconcile 5")
		patch.WithStatus(cachev1alpha1apply.CachedResourceEndpointSliceStatus())
	} else {
		fmt.Println("### endpointsReconciler.reconcile 6")
		patch.WithStatus(cachev1alpha1apply.CachedResourceEndpointSliceStatus().
			WithCachedResourceEndpoints(cachev1alpha1apply.CachedResourceEndpoint().WithURL(rs.url)))
	}
	cluster := logicalcluster.From(slice)
	err = r.patchAPIExportEndpointSlice(ctx, cluster.Path(), patch)
	if err != nil {
		return true, err
	}
	fmt.Println("### endpointsReconciler.reconcile 7")
	return false, nil
}

func (r *endpointsReconciler) updateEndpoints(ctx context.Context, slice *cachev1alpha1.CachedResourceEndpointSlice) (*result, error) {
	fmt.Println("### endpointsReconciler.updateEndpoints 1")

	logger := klog.FromContext(ctx)

	thisShard, err := r.getMyShard()
	if err != nil {
		fmt.Println("### endpointsReconciler.updateEndpoints 2")
		return nil, err
	}

	if thisShard.Spec.VirtualWorkspaceURL == "" {
		// We don't have VW URLs, bail out.
		fmt.Println("### endpointsReconciler.updateEndpoints 3")
		return nil, nil
	}

	exports, err := r.listAPIExportsByCachedResourceEndpointSlice(slice)
	if err != nil {
		fmt.Println("### endpointsReconciler.updateEndpoints 4")
		return nil, err
	}
	bindings, err := r.listAPIBindingsByAPIExports(exports)
	if err != nil {
		fmt.Println("### endpointsReconciler.updateEndpoints 5")
		return nil, err
	}

	if len(bindings) == 0 {
		fmt.Println("### endpointsReconciler.updateEndpoints 6")
		// We don't have any consumers, so clean up all endpoints.
		return &result{
			remove: true,
		}, nil
	}

	shardSelector, err := labels.Parse(slice.Status.ShardSelector)
	if err != nil {
		fmt.Println("### endpointsReconciler.updateEndpoints 7")
		return nil, err
	}
	if !shardSelector.Matches(labels.Set(thisShard.Labels)) {
		fmt.Println("### endpointsReconciler.updateEndpoints 8")
		// We don't belong in the partition, so do nothing.
		return nil, nil
	}

	crPath := logicalcluster.NewPath(slice.Spec.CachedResource.Path)
	if crPath.Empty() {
		crPath = logicalcluster.From(slice).Path()
	}
	cr, err := r.getCachedResource(crPath, slice.Spec.CachedResource.Name)
	if err != nil {
		fmt.Println("### endpointsReconciler.updateEndpoints 9")
		return nil, err
	}

	// Construct the Replication VW URL and try to add it to the slice.

	vwURL, err := url.Parse(thisShard.Spec.VirtualWorkspaceURL)
	if err != nil {
		logger = logging.WithObject(logger, thisShard)
		logger.Error(
			err, "error parsing shard.spec.virtualWorkspaceURL",
			"VirtualWorkspaceURL", thisShard.Spec.VirtualWorkspaceURL,
		)
		fmt.Println("### endpointsReconciler.updateEndpoints 10")
		// Can't do much more...
		return nil, nil
	}

	// Formats the Replication VW URL like so:
	//   <Shard URL>/services/replication/<CachedResource cluster>/<CachedResource name>
	vwURL.Path = path.Join(
		vwURL.Path,
		virtualworkspacesoptions.DefaultRootPathPrefix,
		replicationvw.VirtualWorkspaceName,
		logicalcluster.From(cr).String(),
		cr.Name,
	)
	completeVWAddr := vwURL.String()

	for _, u := range slice.Status.CachedResourceEndpoints {
		if u.URL == completeVWAddr {
			fmt.Println("### endpointsReconciler.updateEndpoints 11")
			// VW URL already in the endpoint slice, nothing to do.
			return nil, nil
		}
	}
	fmt.Println("### endpointsReconciler.updateEndpoints 12")

	return &result{
		url: completeVWAddr,
	}, nil
}
