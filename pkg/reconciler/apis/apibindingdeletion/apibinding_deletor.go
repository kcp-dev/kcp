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

package apibindingdeletion

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/projection"
)

type gvrDeletionMetadata struct {
	// numRemaining is how many instances of the gvr remain
	numRemaining int
	// finalizersToNumRemaining maps finalizers to how many resources are stuck on them
	finalizersToNumRemaining map[string]int
}

type gvrDeletionMetadataTotal struct {
	gvrToNumRemaining        map[schema.GroupVersionResource]int
	finalizersToNumRemaining map[string]int
}

func (c *Controller) deleteAllCRs(ctx context.Context, apibinding *apisv1alpha1.APIBinding) (gvrDeletionMetadataTotal, error) {
	logger := logging.WithObject(klog.FromContext(ctx), apibinding)
	totalResourceRemaining := gvrDeletionMetadataTotal{
		gvrToNumRemaining:        map[schema.GroupVersionResource]int{},
		finalizersToNumRemaining: map[string]int{},
	}

	deleteContentErrs := []error{}
	for _, resource := range apibinding.Status.BoundResources {
		for _, version := range resource.StorageVersions {
			gvr := schema.GroupVersionResource{
				Group:    resource.Group,
				Resource: resource.Resource,
				Version:  version,
			}

			// Don't try to delete projected resources such as tenancy.kcp.dev v1beta1 Workspaces - these are virtual
			// projections and we shouldn't try to delete them. The projections will disappear when the real underlying
			// data (e.g. ClusterWorkspaces) are deleted.
			if projection.Includes(gvr) {
				continue
			}

			logger = logger.WithValues("gvr", gvr.String())
			ctx = klog.NewContext(ctx, logger)
			deletionMetadata, err := c.deleteAllCR(ctx, logicalcluster.From(apibinding), gvr)
			if err != nil {
				deleteContentErrs = append(deleteContentErrs, err)
			}

			if deletionMetadata.numRemaining > 0 {
				totalResourceRemaining.gvrToNumRemaining[gvr] = deletionMetadata.numRemaining
				for finalizer, numRemaining := range deletionMetadata.finalizersToNumRemaining {
					if numRemaining == 0 {
						continue
					}
					totalResourceRemaining.finalizersToNumRemaining[finalizer] += numRemaining
				}
			}
		}
	}

	if len(deleteContentErrs) > 0 {
		return totalResourceRemaining, utilerrors.NewAggregate(deleteContentErrs)
	}

	return totalResourceRemaining, nil
}

func (c *Controller) deleteAllCR(ctx context.Context, clusterName logicalcluster.Name, gvr schema.GroupVersionResource) (gvrDeletionMetadata, error) {
	logger := klog.FromContext(ctx)
	partialList, err := c.listResources(ctx, clusterName, gvr)
	if err != nil {
		return gvrDeletionMetadata{}, err
	}

	deletedNamespaces := sets.String{}
	deleteErrors := []error{}

	for _, item := range partialList.Items {
		if deletedNamespaces.Has(item.GetNamespace()) {
			continue
		}

		// don't retry deleting the same namespace
		deletedNamespaces.Insert(item.GetNamespace())

		// CRs always support deletecollection verb
		if err := c.deleteResources(ctx, clusterName, gvr, item.GetNamespace()); err != nil {
			deleteErrors = append(deleteErrors, err)
			continue
		}
	}

	deleteError := utilerrors.NewAggregate(deleteErrors)
	if deleteError != nil {
		return gvrDeletionMetadata{}, err
	}

	// resource will not be delete immediately, instead of list again, we just return the
	// remaining resources in the first list and recheck later.
	if len(partialList.Items) == 0 {
		// we're done
		return gvrDeletionMetadata{numRemaining: 0}, nil
	}

	// use the list to find the finalizers
	finalizersToNumRemaining := map[string]int{}
	for _, item := range partialList.Items {
		for _, finalizer := range item.GetFinalizers() {
			finalizersToNumRemaining[finalizer]++
		}
	}

	logger.WithValues("finalizers", finalizersToNumRemaining).V(5).Info("estimate is present")
	return gvrDeletionMetadata{
		numRemaining:             len(partialList.Items),
		finalizersToNumRemaining: finalizersToNumRemaining,
	}, nil
}
