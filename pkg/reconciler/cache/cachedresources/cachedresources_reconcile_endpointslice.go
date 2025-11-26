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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
)

// endpointSlice creates CachedResourceEndopointSlice for the published resource.
type endpointSlice struct {
	getEndpointSlice    func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	createEndpointSlice func(ctx context.Context, path logicalcluster.Path, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice) error
}

func (r *endpointSlice) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	if !cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	if _, ok := cachedResource.Annotations[cachev1alpha1.CachedResourceEndpointSliceSkipAnnotation]; ok {
		return reconcileStatusContinue, nil
	}

	clusterName := logicalcluster.From(cachedResource)
	clusterPath := cachedResource.Annotations[core.LogicalClusterPathAnnotationKey]

	_, err := r.getEndpointSlice(ctx, clusterName, cachedResource.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the CachedResourceEndpointSlice.
			cachedResourceEndpointSlice := cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: cachedResource.Name,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
							Kind:       "CachedResource",
							Name:       cachedResource.Name,
							UID:        cachedResource.UID,
						},
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Path: clusterPath,
						Name: cachedResource.Name,
					},
				},
			}
			if err = r.createEndpointSlice(ctx, clusterName.Path(), &cachedResourceEndpointSlice); err != nil {
				return reconcileStatusStopAndRequeue, fmt.Errorf("error creating CachedResourceEndpointSlice for CachedResource %s|%s: %w", clusterName, cachedResource.Name, err)
			}
		} else {
			return reconcileStatusStopAndRequeue, fmt.Errorf("error getting CachedResourceEndpointSlice for CachedResource %s|%s: %w", clusterName, cachedResource.Name, err)
		}
	}

	return reconcileStatusContinue, nil
}
