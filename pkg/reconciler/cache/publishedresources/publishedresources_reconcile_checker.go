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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// checker checks if the published resource is valid.
type checker struct {
	listSelectedResources func(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (*unstructured.UnstructuredList, error)
}

func (r *checker) reconcile(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	selectedResources, err := r.listSelectedResources(ctx, publishedResource)
	if err != nil {
		return reconcileStatusContinue, err
	}

	if len(selectedResources.Items) == 0 {
		logger.V(2).Info("No selected resources found for published resource", "publishedResource", publishedResource.Name)
		conditions.MarkFalse(publishedResource, cachev1alpha1.PublishedResourceValid, cachev1alpha1.PublishedResourceValidNoResources, conditionsv1alpha1.ConditionSeverityError, "No resources found")
		return reconcileStatusContinue, nil
	} else {
		conditions.MarkTrue(publishedResource, cachev1alpha1.PublishedResourceValid)
	}

	return reconcileStatusContinue, nil
}
