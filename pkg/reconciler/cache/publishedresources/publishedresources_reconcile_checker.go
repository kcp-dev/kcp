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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// counters counts the number of resources in the local and cache and updates the status.
type counter struct {
	listSelectedLocalResources func(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (*unstructured.UnstructuredList, error)
	listSelectedCacheResources func(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (*cachev1alpha1.PublishedObjectList, error)
}

func (r *counter) reconcile(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (reconcileStatus, error) {
	if publishedResource.Status.ResourceCounts == nil {
		publishedResource.Status.ResourceCounts = &cachev1alpha1.ResourceCount{
			Cache: 0,
			Local: 0,
		}
	}

	selectedLocalResources, err := r.listSelectedLocalResources(ctx, publishedResource)
	if err != nil && !errors.IsNotFound(err) {
		return reconcileStatusContinue, err
	}

	selectedCacheResources, err := r.listSelectedCacheResources(ctx, publishedResource)
	if err != nil && !errors.IsNotFound(err) {
		return reconcileStatusContinue, err
	}

	if selectedLocalResources != nil {
		publishedResource.Status.ResourceCounts.Local = len(selectedLocalResources.Items)
	}
	if selectedCacheResources != nil {
		publishedResource.Status.ResourceCounts.Cache = len(selectedCacheResources.Items)
	}

	conditions.MarkTrue(publishedResource, cachev1alpha1.PublishedResourceValid)

	return reconcileStatusContinue, nil
}
