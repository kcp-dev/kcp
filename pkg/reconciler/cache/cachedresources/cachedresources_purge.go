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

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
)

// purge is a reconciler that purges the cache resources for a published resource.
type purge struct {
	deleteSelectedCacheResources func(ctx context.Context, CachedResource *cachev1alpha1.CachedResource) error
}

func (r *purge) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	if cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	err := r.deleteSelectedCacheResources(ctx, cachedResource)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	return reconcileStatusContinue, nil
}
