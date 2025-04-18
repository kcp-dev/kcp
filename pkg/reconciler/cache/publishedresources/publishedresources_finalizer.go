/*
Copyright 2026 The KCP Authors.

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
)

type finalizer struct{}

func (r *finalizer) reconcile(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (reconcileStatus, error) {
	if publishedResource.DeletionTimestamp.IsZero() {
		if !slices.Contains(publishedResource.Finalizers, cachev1alpha1.PublishedResourceFinalizer) {
			publishedResource.Finalizers = append(publishedResource.Finalizers, cachev1alpha1.PublishedResourceFinalizer)
			return reconcileStatusStopAndRequeue, nil
		}
	}
	return reconcileStatusContinue, nil
}
