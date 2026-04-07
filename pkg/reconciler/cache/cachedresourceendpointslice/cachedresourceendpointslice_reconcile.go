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

package cachedresourceendpointslice

import (
	"context"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	cachev1alpha1helper "github.com/kcp-dev/sdk/apis/cache/v1alpha1/helper"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func (c *controller) reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) error {
	cachedResourcePath := logicalcluster.NewPath(endpoints.Spec.CachedResource.Path)
	if cachedResourcePath.Empty() {
		cachedResourcePath = logicalcluster.From(endpoints).Path()
	}

	_, err := c.getCachedResource(cachedResourcePath, endpoints.Spec.CachedResource.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Don't keep the endpoints if the CachedResource has been deleted.
			endpoints.Status.CachedResourceEndpoints = nil
			conditions.MarkFalse(
				endpoints,
				cachev1alpha1.CachedResourceValid,
				cachev1alpha1.CachedResourceNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Error getting CachedResource %s|%s",
				cachedResourcePath,
				endpoints.Spec.CachedResource.Name,
			)
			// No need to try again.
			return nil
		}
		conditions.MarkFalse(
			endpoints,
			cachev1alpha1.CachedResourceValid,
			cachev1alpha1.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Error getting CachedResource %s|%s",
			cachedResourcePath,
			endpoints.Spec.CachedResource.Name,
		)
		return err
	}
	conditions.MarkTrue(endpoints, cachev1alpha1.CachedResourceValid)

	// Check that the referenced APIExport exists and references this endpoint slice.
	apiExportPath := logicalcluster.NewPath(endpoints.Spec.APIExport.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(endpoints).Path()
	}
	export, err := c.getAPIExport(apiExportPath, endpoints.Spec.APIExport.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			endpoints.Status.CachedResourceEndpoints = nil
			conditions.MarkFalse(
				endpoints,
				cachev1alpha1.APIExportValid,
				cachev1alpha1.APIExportInvalidReferenceReason,
				conditionsv1alpha1.ConditionSeverityError,
				"APIExport %s|%s not found",
				apiExportPath,
				endpoints.Spec.APIExport.Name,
			)
			// No need to try again.
			return nil
		}
		conditions.MarkFalse(
			endpoints,
			cachev1alpha1.APIExportValid,
			cachev1alpha1.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Error getting APIExport %s|%s: %v",
			apiExportPath,
			endpoints.Spec.APIExport.Name,
			err,
		)
		return err
	}
	referencedByExport := slices.ContainsFunc(export.Spec.Resources, func(res apisv1alpha2.ResourceSchema) bool {
		return cachev1alpha1helper.IsCachedResourceEndpointSliceResourceStorage(&res.Storage) &&
			res.Storage.Virtual.Reference.Name == endpoints.Name
	})
	if !referencedByExport {
		endpoints.Status.CachedResourceEndpoints = nil
		conditions.MarkFalse(
			endpoints,
			cachev1alpha1.APIExportValid,
			cachev1alpha1.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			"APIExport %s|%s does not reference this CachedResourceEndpointSlice",
			apiExportPath,
			endpoints.Spec.APIExport.Name,
		)
		return nil
	}
	// TODO(gman0): is this a good place to check that the identity matches the one in export.Spec.Resources[].Storage.Virtual.IdentityHash?
	conditions.MarkTrue(endpoints, cachev1alpha1.APIExportValid)

	// Check the partition selector.
	var selector labels.Selector
	if endpoints.Spec.Partition != "" {
		partition, err := c.getPartition(logicalcluster.From(endpoints), endpoints.Spec.Partition)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Don't keep the endpoints if the Partition has been deleted and is still referenced.
				endpoints.Status.CachedResourceEndpoints = nil
				conditions.MarkFalse(
					endpoints,
					cachev1alpha1.PartitionValid,
					cachev1alpha1.PartitionInvalidReferenceReason,
					conditionsv1alpha1.ConditionSeverityError,
					"%v",
					err,
				)
				// No need to try again.
				return nil
			}
			conditions.MarkFalse(
				endpoints,
				cachev1alpha1.PartitionValid,
				cachev1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"%v",
				err,
			)
			return err
		}
		selector, err = metav1.LabelSelectorAsSelector(partition.Spec.Selector)
		if err != nil {
			conditions.MarkFalse(
				endpoints,
				cachev1alpha1.PartitionValid,
				cachev1alpha1.PartitionInvalidReferenceReason,
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

	conditions.MarkTrue(endpoints, cachev1alpha1.PartitionValid)

	endpoints.Status.ShardSelector = selector.String()

	return nil
}
