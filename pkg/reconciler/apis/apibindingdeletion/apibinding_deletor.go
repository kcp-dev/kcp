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
	"fmt"
	"sort"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion/deletion"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	DeletionRecheckEstimateSeconds = 5

	// ResourceDeletionFailedReason is the reason for condition BindingResourceDeleteSuccess that deletion of
	// some CRs is failed
	ResourceDeletionFailedReason = "ResourceDeletionFailed"

	// ResourceRemainingReason is the reason for condition BindingResourceDeleteSuccess that some CR resource still
	// exists when apibinding is deleting
	ResourceRemainingReason = "SomeResourcesRemain"

	// ResourceFinalizersRemainReason is the reason for condition BindingResourceDeleteSuccess that finalizers on some
	// CRs still exist.
	ResourceFinalizersRemainReason = "SomeFinalizersRemain"
)

type gvrDeletionMetadata struct {
	// numRemaining is how many instances of the gvr remain
	numRemaining int
	// finalizersToNumRemaining maps finalizers to how many resources are stuck on them
	finalizersToNumRemaining map[string]int
}

func (c *Controller) deleteAllCRs(ctx context.Context, apibinding *apisv1alpha1.APIBinding) error {

	gvrToNumRemaining := map[schema.GroupVersionResource]int{}
	finalizersToNumRemaining := map[string]int{}

	deleteContentErrs := []error{}
	for _, resource := range apibinding.Status.BoundResources {
		for _, version := range resource.StorageVersions {
			gvr := schema.GroupVersionResource{
				Group:    resource.Group,
				Resource: resource.Resource,
				Version:  version,
			}

			deletionMetadata, err := c.deleteAllCR(ctx, logicalcluster.From(apibinding), gvr)
			if err != nil {
				deleteContentErrs = append(deleteContentErrs, err)
			}

			if deletionMetadata.numRemaining > 0 {
				gvrToNumRemaining[gvr] = deletionMetadata.numRemaining
				for finalizer, numRemaining := range deletionMetadata.finalizersToNumRemaining {
					if numRemaining == 0 {
						continue
					}
					finalizersToNumRemaining[finalizer] = finalizersToNumRemaining[finalizer] + numRemaining
				}
			}
		}
	}

	if len(deleteContentErrs) > 0 {
		conditions.MarkFalse(
			apibinding,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceDeletionFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			utilerrors.NewAggregate(deleteContentErrs).Error(),
		)

		return utilerrors.NewAggregate(deleteContentErrs)
	}

	if len(finalizersToNumRemaining) != 0 {
		remainingByFinalizer := []string{}
		for finalizer, numRemaining := range finalizersToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingByFinalizer = append(remainingByFinalizer, fmt.Sprintf("%s in %d resource instances", finalizer, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingByFinalizer)
		conditions.MarkFalse(
			apibinding,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceFinalizersRemainReason,
			conditionsv1alpha1.ConditionSeverityError,
			fmt.Sprintf("Some content in the workspace has finalizers remaining: %s", strings.Join(remainingByFinalizer, ", ")),
		)

		return &deletion.ResourcesRemainingError{Estimate: DeletionRecheckEstimateSeconds}
	}

	if len(gvrToNumRemaining) != 0 {
		remainingResources := []string{}
		for gvr, numRemaining := range gvrToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingResources = append(remainingResources, fmt.Sprintf("%s.%s has %d resource instances", gvr.Resource, gvr.Group, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingResources)

		conditions.MarkFalse(
			apibinding,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceRemainingReason,
			conditionsv1alpha1.ConditionSeverityError,
			fmt.Sprintf("Some resources are remaining: %s", strings.Join(remainingResources, ", ")),
		)

		return &deletion.ResourcesRemainingError{Estimate: DeletionRecheckEstimateSeconds}
	}

	conditions.MarkTrue(apibinding, apisv1alpha1.BindingResourceDeleteSuccess)

	return nil
}

func (c *Controller) deleteAllCR(ctx context.Context, clusterName logicalcluster.Name, gvr schema.GroupVersionResource) (gvrDeletionMetadata, error) {
	partialList, err := c.metadataClient.Resource(gvr).Namespace(metav1.NamespaceAll).List(genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: clusterName}), metav1.ListOptions{})
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
		background := metav1.DeletePropagationBackground
		opts := metav1.DeleteOptions{PropagationPolicy: &background}

		// CRs always support deletecollection verb
		if err := c.metadataClient.Resource(gvr).Namespace(item.GetNamespace()).DeleteCollection(
			genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: clusterName}), opts, metav1.ListOptions{}); err != nil {
			deleteErrors = append(deleteErrors, err)
			continue
		}
	}

	deleteError := utilerrors.NewAggregate(deleteErrors)
	if deleteError != nil {
		return gvrDeletionMetadata{}, err
	}

	// list again so we know how many left resources
	partialList, err = c.metadataClient.Resource(gvr).Namespace(metav1.NamespaceAll).List(genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: clusterName}), metav1.ListOptions{})
	if err != nil {
		return gvrDeletionMetadata{}, err
	}

	if len(partialList.Items) == 0 {
		// we're done
		return gvrDeletionMetadata{numRemaining: 0}, nil
	}

	// use the list to find the finalizers
	finalizersToNumRemaining := map[string]int{}
	for _, item := range partialList.Items {
		for _, finalizer := range item.GetFinalizers() {
			finalizersToNumRemaining[finalizer] = finalizersToNumRemaining[finalizer] + 1
		}
	}

	klog.V(5).Infof("apibinding deletion controller - deleteAllCR - estimate is present - workspace: %s, gvr: %v, finalizers: %v", clusterName, gvr, finalizersToNumRemaining)
	return gvrDeletionMetadata{
		numRemaining:             len(partialList.Items),
		finalizersToNumRemaining: finalizersToNumRemaining,
	}, nil
}
