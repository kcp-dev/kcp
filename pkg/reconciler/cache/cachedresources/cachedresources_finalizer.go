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

package cachedresources

import (
	"context"
	"slices"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// finalizer is a reconciler that ensures the finalizer is present on the published resource and object is marked as deleting
// If is done deleting - it removes the finalizer.
type finalizer struct{}

func (r *finalizer) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	switch {
	case !cachedResource.DeletionTimestamp.IsZero() && cachedResource.Status.Phase == cachev1alpha1.CachedResourcePhaseDeleted: // case 1: Resource is in Deleted phase, remove finalizer
		if slices.Contains(cachedResource.Finalizers, cachev1alpha1.CachedResourceFinalizer) {
			cachedResource.Finalizers = slices.DeleteFunc(cachedResource.Finalizers, func(f string) bool {
				return f == cachev1alpha1.CachedResourceFinalizer
			})
		}
		return reconcileStatusStopAndRequeue, nil
	case !cachedResource.DeletionTimestamp.IsZero() && cachedResource.Status.Phase != cachev1alpha1.CachedResourcePhaseDeleting: // case 2: Resource is being deleted - mark as deleting
		// Case 1: Resource is being deleted
		conditions.MarkFalse(cachedResource, cachev1alpha1.CachedResourceValid, cachev1alpha1.CachedResourceValidDeleting, conditionsv1alpha1.ConditionSeverityError, "Published resource deleting")
		cachedResource.Status.Phase = cachev1alpha1.CachedResourcePhaseDeleting
		return reconcileStatusStopAndRequeue, nil
	default: // case 3: Resource is not deleted, ensure finalizer is present
		if !slices.Contains(cachedResource.Finalizers, cachev1alpha1.CachedResourceFinalizer) {
			cachedResource.Finalizers = append(cachedResource.Finalizers, cachev1alpha1.CachedResourceFinalizer)
			return reconcileStatusStopAndRequeue, nil
		}
	}

	return reconcileStatusContinue, nil
}
