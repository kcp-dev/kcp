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

package publishedresources

import (
	"context"
	"slices"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// finalizer is a reconciler that ensures the finalizer is present on the published resource and obejct is marked as deleting
// If is done deleting - it removes the finalizer.
type finalizer struct{}

func (r *finalizer) reconcile(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (reconcileStatus, error) {
	switch {
	case !publishedResource.DeletionTimestamp.IsZero() && publishedResource.Status.Phase == cachev1alpha1.PublishedResourcePhaseDeleted: // case 1: Resource is in Deleted phase, remove finalizer
		if slices.Contains(publishedResource.Finalizers, cachev1alpha1.PublishedResourceFinalizer) {
			publishedResource.Finalizers = slices.DeleteFunc(publishedResource.Finalizers, func(f string) bool {
				return f == cachev1alpha1.PublishedResourceFinalizer
			})
		}
		return reconcileStatusStopAndRequeue, nil
	case !publishedResource.DeletionTimestamp.IsZero() && publishedResource.Status.Phase != cachev1alpha1.PublishedResourcePhaseDeleting: // case 2: Resource is being deleted - mark as deleting
		// Case 1: Resource is being deleted
		conditions.MarkFalse(publishedResource, cachev1alpha1.PublishedResourceValid, cachev1alpha1.PublishedResourceValidDeleting, conditionsv1alpha1.ConditionSeverityError, "Published resource deleting")
		publishedResource.Status.Phase = cachev1alpha1.PublishedResourcePhaseDeleting
		return reconcileStatusStopAndRequeue, nil
	default: // case 3: Resource is not deleted, ensure finalizer is present
		if !slices.Contains(publishedResource.Finalizers, cachev1alpha1.PublishedResourceFinalizer) {
			publishedResource.Finalizers = append(publishedResource.Finalizers, cachev1alpha1.PublishedResourceFinalizer)
			return reconcileStatusStopAndRequeue, nil
		}
	}

	return reconcileStatusContinue, nil
}
