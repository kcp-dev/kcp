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

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

type endpointsReconciler struct {
	getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getPartition func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
}

func (c *controller) reconcile(ctx context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error {
	r := &endpointsReconciler{
		getAPIExport: c.getAPIExport,
		getPartition: c.getPartition,
	}

	return r.reconcile(ctx, apiExportEndpointSlice)
}

func (r *endpointsReconciler) reconcile(_ context.Context, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error {
	// TODO(mjudeikis): Remove this at some point once we confident that we don't use it anymore.
	conditions.Delete(
		apiExportEndpointSlice,
		apisv1alpha1.ErrorGeneratingURLsReason,
	)

	// Get APIExport
	apiExportPath := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	_, err := r.getAPIExport(apiExportPath, apiExportEndpointSlice.Spec.APIExport.Name)
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
					"%v",
					err,
				)
				// No need to try again
				return nil
			} else {
				conditions.MarkFalse(
					apiExportEndpointSlice,
					apisv1alpha1.PartitionValid,
					apisv1alpha1.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"%v",
					err,
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
				"%v",
				err,
			)
			return err
		}
	}
	if selector == nil {
		selector = labels.Everything()
	}

	conditions.MarkTrue(apiExportEndpointSlice, apisv1alpha1.PartitionValid)

	// We presenrve selector in the status for url generation. Else we don't know partition selector
	// without propagating partitions over the cache.
	apiExportEndpointSlice.Status.ShardSelector = selector.String()

	return nil
}
