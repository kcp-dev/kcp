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

package cachedresources

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestSchema(t *testing.T) {
	tests := map[string]struct {
		CachedResource     *cachev1alpha1.CachedResource
		reconciler         *validSchema
		expectedErr        error
		expectedStatus     reconcileStatus
		expectedConditions conditionsv1alpha1.Conditions
	}{
		"resource not found": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "none",
						Version:  "v1",
						Resource: "nonexistent",
					},
				},
			},
			reconciler: &validSchema{
				getResourceScope: func(gvr schema.GroupVersionResource) (meta.RESTScope, error) {
					return nil, &meta.NoResourceMatchError{PartialResource: gvr}
				},
			},
			expectedErr: fmt.Errorf("no matches for none/v1, Resource=nonexistent"),
		},
		"resource is namespace-scoped and check fails": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "foo.dev",
						Version:  "v1",
						Resource: "namespaced",
					},
				},
			},
			reconciler: &validSchema{
				getResourceScope: func(gvr schema.GroupVersionResource) (meta.RESTScope, error) {
					return meta.RESTScopeNamespace, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceValid,
					cachev1alpha1.ResourceNotClusterScoped,
					conditionsv1alpha1.ConditionSeverityError,
					"Resource namespaced.foo.dev is not cluster-scoped",
				),
			},
		},
		"resource is cluster-scoped and check succeeds": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "foo.dev",
						Version:  "v1",
						Resource: "clusterscoped",
					},
				},
			},
			reconciler: &validSchema{
				getResourceScope: func(gvr schema.GroupVersionResource) (meta.RESTScope, error) {
					return meta.RESTScopeRoot, nil
				},
			},
			expectedStatus: reconcileStatusContinue,
		},
	}

	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			status, err := tt.reconciler.reconcile(context.Background(), tt.CachedResource)

			resetLastTransitionTime(tt.expectedConditions)
			resetLastTransitionTime(tt.CachedResource.Status.Conditions)

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Equal(t, tt.expectedErr.Error(), err.Error())
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expectedStatus, status, "reconcile status mismatch")
			require.Equal(t, tt.expectedConditions, tt.CachedResource.Status.Conditions, "conditions mismatch")
		})
	}
}
