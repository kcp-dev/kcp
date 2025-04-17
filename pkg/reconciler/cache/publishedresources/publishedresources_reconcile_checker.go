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

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
)

// checker checks if the published resource is valid.
type checker struct {
	getMountObject func(ctx context.Context, cluster logicalcluster.Path, ref *cachev1alpha1.PublishedResource) (*unstructured.Unstructured, error)
}

func (r *checker) reconcile(ctx context.Context, workspace *cachev1alpha1.PublishedResource) (reconcileStatus, error) {

	return reconcileStatusContinue, nil
}
