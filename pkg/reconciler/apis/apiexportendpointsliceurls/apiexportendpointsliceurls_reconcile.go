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

package apiexportendpointsliceurls

import (
	"context"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	apiexportbuilder "github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	apisv1alpha1apply "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/apis/v1alpha1"
)

type endpointsReconciler struct {
	getMyShard                  func() (*corev1alpha1.Shard, error)
	getAPIExport                func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	listAPIBindingsByAPIExport  func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error)
	patchAPIExportEndpointSlice func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error
	shardName                   string
}

type result struct {
	url    string
	remove bool
}

func (c *controller) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) (bool, error) {
	r := &endpointsReconciler{
		getMyShard:                  c.getMyShard,
		getAPIExport:                c.getAPIExport,
		listAPIBindingsByAPIExport:  c.listAPIBindingsByAPIExport,
		shardName:                   c.shardName,
		patchAPIExportEndpointSlice: c.patchAPIExportEndpointSlice,
	}

	return r.reconcile(ctx, apiExportEndpointSlice)
}

func (r *endpointsReconciler) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) (bool, error) {
	// we only continue if all conditions are set to true. As this is more of the secondary controller,
	// we don't want to do anything if the primary controller is not ready.
	for _, condition := range apiExportEndpointSlice.Status.Conditions {
		if !conditions.IsTrue(apiExportEndpointSlice, condition.Type) {
			return false, nil
		}
	}

	selector, err := labels.Parse(apiExportEndpointSlice.Status.ShardSelector)
	if err != nil {
		return false, err
	}

	apiExportPath := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	apiExport, err := r.getAPIExport(apiExportPath, apiExportEndpointSlice.Spec.APIExport.Name)
	if err != nil {
		return true, err
	}

	shard, err := r.getMyShard()
	if err != nil {
		return true, err
	}

	rs, err := r.updateEndpoints(ctx, apiExportEndpointSlice, apiExport, shard, selector)
	if err != nil {
		return true, err
	}
	if rs == nil { // no change, nothing to do.
		return false, nil
	}

	// Patch the object
	patch := apisv1alpha1apply.APIExportEndpointSlice(apiExportEndpointSlice.Name)
	if rs.remove {
		patch.WithStatus(apisv1alpha1apply.APIExportEndpointSliceStatus())
	} else {
		patch.WithStatus(apisv1alpha1apply.APIExportEndpointSliceStatus().
			WithAPIExportEndpoints(apisv1alpha1apply.APIExportEndpoint().WithURL(rs.url)))
	}
	cluster := logicalcluster.From(apiExportEndpointSlice)
	err = r.patchAPIExportEndpointSlice(ctx, cluster.Path(), patch)
	if err != nil {
		return true, err
	}
	return false, nil
}

func (r *endpointsReconciler) updateEndpoints(ctx context.Context,
	apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice,
	apiExport *apisv1alpha1.APIExport,
	shard *corev1alpha1.Shard,
	selector labels.Selector,
) (*result, error) {
	logger := klog.FromContext(ctx)
	var rs result
	if shard.Spec.VirtualWorkspaceURL == "" {
		return nil, nil
	}

	// Check if we have local consumers
	bindings, err := r.listAPIBindingsByAPIExport(apiExport)
	if err != nil {
		return nil, err
	}

	if selector.Matches(labels.Set(shard.Labels)) { // we are in partition
		if len(bindings) == 0 { // we have no consumers
			return &result{
				remove: true,
			}, nil
		} // This falls through to the next block to update the URL.
	} else { // we are not in partition
		if len(bindings) == 0 { // we have no consumers, we can remove the endpoint
			return &result{
				remove: true,
			}, nil
		} else {
			// we not in partition, but we have consumers.
			// Do nothing, as we are on the way to be orphaned.
			// If we remove url, where is chance we gonna kill controllers on their way out.
			return nil, nil
		}
	}

	u, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
	if err != nil {
		// Should never happen
		logger = logging.WithObject(logger, shard)
		logger.Error(
			err, "error parsing shard.spec.virtualWorkspaceURL",
			"VirtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL,
		)
		return nil, nil
	}

	u.Path = path.Join(
		u.Path,
		virtualworkspacesoptions.DefaultRootPathPrefix,
		apiexportbuilder.VirtualWorkspaceName,
		logicalcluster.From(apiExport).String(),
		apiExport.Name,
	)

	rs.url = u.String()

	for _, u := range apiExportEndpointSlice.Status.APIExportEndpoints {
		if u.URL == rs.url {
			return nil, nil
		}
	}

	return &rs, nil
}
