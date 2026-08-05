/*
Copyright 2025 The kcp Authors.

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

package clustercachedresources

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

type validSchema struct {
	getResourceScope func(gvr schema.GroupVersionResource) (meta.RESTScope, error)
}

func (r *validSchema) reconcile(ctx context.Context, clusterCachedResource *cachev1alpha1.ClusterCachedResource) (reconcileStatus, error) {
	wrappedGVR := schema.GroupVersionResource(clusterCachedResource.Spec.GroupVersionResource)

	scope, err := r.getResourceScope(wrappedGVR)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	if scope == meta.RESTScopeRoot {
		return reconcileStatusContinue, nil
	}

	conditions.MarkFalse(
		clusterCachedResource,
		cachev1alpha1.ClusterCachedResourceValid,
		cachev1alpha1.ResourceNotClusterScoped,
		conditionsv1alpha1.ConditionSeverityError,
		"Resource %s is not cluster-scoped",
		wrappedGVR.GroupResource(),
	)

	return reconcileStatusStop, nil
}
