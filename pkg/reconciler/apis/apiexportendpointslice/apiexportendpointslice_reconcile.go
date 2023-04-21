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

package apiexportendpointslice

import (
	"context"
	"net/url"
	"path"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	apiexportbuilder "github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

type endpointsReconciler struct {
	listShards   func(selector labels.Selector) ([]*corev1alpha1.Shard, error)
	getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getPartition func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
}

func (c *controller) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error {
	r := &endpointsReconciler{
		listShards:   c.listShards,
		getAPIExport: c.getAPIExport,
		getPartition: c.getPartition,
	}

	return r.reconcile(ctx, apiExportEndpointSlice)
}

func (r *endpointsReconciler) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error {
	// TODO (fgiloux): When the information is available in the cache server
	// check if at least one APIBinding is bound in the shard to the APIExport referenced by the APIExportEndpointSlice.
	// If so, add the respective endpoint to the status.
	// For now the unfiltered list is added.

	// Get APIExport
	apiExportPath := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	apiExport, err := r.getAPIExport(apiExportPath, apiExportEndpointSlice.Spec.APIExport.Name)
	if err != nil {
		reason := apisv1alpha1.InternalErrorReason
		if errors.IsNotFound(err) {
			// Don't keep the endpoints if the APIExport has been deleted
			apiExportEndpointSlice.Status.APIExportEndpoints = nil
			conditions.MarkFalse(
				apiExportEndpointSlice,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.APIExportNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Error getting APIExport %s|%s",
				apiExportPath,
				apiExportEndpointSlice.Spec.APIExport.Name,
			)
			conditions.MarkFalse(
				apiExportEndpointSlice,
				apisv1alpha1.APIExportEndpointSliceURLsReady,
				apisv1alpha1.ErrorGeneratingURLsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"",
			)
			// No need to try again
			return nil
		} else {
			conditions.MarkFalse(
				apiExportEndpointSlice,
				apisv1alpha1.APIExportValid,
				reason,
				conditionsv1alpha1.ConditionSeverityError,
				"Error getting APIExport %s|%s",
				apiExportPath,
				apiExportEndpointSlice.Spec.APIExport.Name,
			)
			conditions.MarkUnknown(
				apiExportEndpointSlice,
				apisv1alpha1.APIExportEndpointSliceURLsReady,
				apisv1alpha1.ErrorGeneratingURLsReason,
				"",
			)
			return err
		}
	}
	conditions.MarkTrue(apiExportEndpointSlice, apisv1alpha1.APIExportValid)

	// Get selector from Partition
	var selector labels.Selector
	if apiExportEndpointSlice.Spec.Partition != "" {
		partition, err := r.getPartition(logicalcluster.From(apiExportEndpointSlice), apiExportEndpointSlice.Spec.Partition)
		if err != nil {
			if errors.IsNotFound(err) {
				// Don't keep the endpoints if the Partition has been deleted
				// and is still referenced
				apiExportEndpointSlice.Status.APIExportEndpoints = nil
				conditions.MarkFalse(
					apiExportEndpointSlice,
					apisv1alpha1.PartitionValid,
					apisv1alpha1.PartitionInvalidReferenceReason,
					conditionsv1alpha1.ConditionSeverityError,
					err.Error(),
				)
				conditions.MarkFalse(
					apiExportEndpointSlice,
					apisv1alpha1.APIExportEndpointSliceURLsReady,
					apisv1alpha1.ErrorGeneratingURLsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"",
				)
				// No need to try again
				return nil
			} else {
				conditions.MarkFalse(
					apiExportEndpointSlice,
					apisv1alpha1.PartitionValid,
					apisv1alpha1.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					err.Error(),
				)
				conditions.MarkUnknown(
					apiExportEndpointSlice,
					apisv1alpha1.APIExportEndpointSliceURLsReady,
					apisv1alpha1.ErrorGeneratingURLsReason,
					"",
				)
				return err
			}
		}
		selector, err = metav1.LabelSelectorAsSelector(partition.Spec.Selector)
		if err != nil {
			conditions.MarkFalse(
				apiExportEndpointSlice,
				apisv1alpha1.PartitionValid,
				apisv1alpha1.PartitionInvalidReferenceReason,
				conditionsv1alpha1.ConditionSeverityError,
				err.Error(),
			)
			conditions.MarkFalse(
				apiExportEndpointSlice,
				apisv1alpha1.APIExportEndpointSliceURLsReady,
				apisv1alpha1.ErrorGeneratingURLsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"",
			)
			return err
		}
	}
	if selector == nil {
		selector = labels.Everything()
	}
	conditions.MarkTrue(apiExportEndpointSlice, apisv1alpha1.PartitionValid)

	// Get shards
	shards, err := r.listShards(selector)
	if err != nil {
		conditions.MarkFalse(
			apiExportEndpointSlice,
			apisv1alpha1.APIExportEndpointSliceURLsReady,
			apisv1alpha1.ErrorGeneratingURLsReason,
			conditionsv1alpha1.ConditionSeverityError,
			"error listing shards",
		)
		return err
	}

	if err = r.updateEndpoints(ctx, apiExportEndpointSlice, apiExport, shards); err != nil {
		conditions.MarkFalse(
			apiExportEndpointSlice,
			apisv1alpha1.APIExportEndpointSliceURLsReady,
			apisv1alpha1.ErrorGeneratingURLsReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
		return err
	}
	conditions.MarkTrue(apiExportEndpointSlice, apisv1alpha1.APIExportEndpointSliceURLsReady)

	return nil
}

func (r *endpointsReconciler) updateEndpoints(ctx context.Context,
	apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice,
	apiExport *apisv1alpha1.APIExport,
	shards []*corev1alpha1.Shard) error {
	logger := klog.FromContext(ctx)
	desiredURLs := sets.New[string]()
	for _, shard := range shards {
		if shard.Spec.VirtualWorkspaceURL == "" {
			continue
		}

		u, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
		if err != nil {
			// Should never happen
			logger = logging.WithObject(logger, shard)
			logger.Error(
				err, "error parsing shard.spec.virtualWorkspaceURL",
				"VirtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL,
			)

			continue
		}

		u.Path = path.Join(
			u.Path,
			virtualworkspacesoptions.DefaultRootPathPrefix,
			apiexportbuilder.VirtualWorkspaceName,
			logicalcluster.From(apiExport).String(),
			apiExport.Name,
		)

		desiredURLs.Insert(u.String())
	}

	apiExportEndpointSlice.Status.APIExportEndpoints = nil
	for _, u := range sets.List[string](desiredURLs) {
		apiExportEndpointSlice.Status.APIExportEndpoints = append(apiExportEndpointSlice.Status.APIExportEndpoints, apisv1alpha1.APIExportEndpoint{
			URL: u,
		})
	}

	return nil
}
