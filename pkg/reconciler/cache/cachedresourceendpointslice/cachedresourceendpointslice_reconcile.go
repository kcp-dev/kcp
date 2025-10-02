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

package cachedresourceendpointslice

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	r := &endpointsReconciler{
		getCachedResource: c.getCachedResource,
		getPartition:      c.getPartition,
	}

	return r.reconcile(ctx, endpoints)
}

type endpointsReconciler struct {
	getCachedResource func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error)
	getPartition      func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
}

func (r *endpointsReconciler) reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	_, err := r.getCachedResource(logicalcluster.From(endpoints).Path(), endpoints.Spec.CachedResource.Name)
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
				logicalcluster.From(endpoints),
				endpoints.Spec.CachedResource.Name,
			)
			// No need to try again.
			return false, nil
		} else {
			conditions.MarkFalse(
				endpoints,
				cachev1alpha1.CachedResourceValid,
				cachev1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Error getting CachedResource %s|%s",
				logicalcluster.From(endpoints),
				endpoints.Spec.CachedResource.Name,
			)
			return true, err
		}
	}
	conditions.MarkTrue(endpoints, cachev1alpha1.CachedResourceValid)

	// Check the partition selector.
	var selector labels.Selector
	if endpoints.Spec.Partition != "" {
		partition, err := r.getPartition(logicalcluster.From(endpoints), endpoints.Spec.Partition)
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
				return false, nil
			} else {
				conditions.MarkFalse(
					endpoints,
					cachev1alpha1.PartitionValid,
					cachev1alpha1.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"%v",
					err,
				)
				return true, err
			}
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
			return true, err
		}
	}
	if selector == nil {
		selector = labels.Everything()
	}

	conditions.MarkTrue(endpoints, cachev1alpha1.PartitionValid)

	endpoints.Status.ShardSelector = selector.String()

	return true, err
}
