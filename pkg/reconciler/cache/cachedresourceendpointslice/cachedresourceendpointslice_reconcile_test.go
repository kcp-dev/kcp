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

package cachedresourceendpointslice

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
)

func TestReconcile(t *testing.T) {
	const workspacePath = "root:org:ws"
	tests := map[string]struct {
		keyMissing                bool
		cachedResourceMissing     bool
		partitionMissing          bool
		cachedResourceInternalErr bool
		listShardsError           error
		errorReason               string

		wantError                  bool
		wantVerifyFailure          bool
		wantCachedResourceValid    bool
		wantPartitionValid         bool
		wantCachedResourceNotValid bool
		wantPartitionNotValid      bool

		sliceMutator               func(slice *cachev1alpha1.CachedResourceEndpointSlice)
		expectedCachedResourcePath string
	}{
		"CachedResourceValid set to false when CachedResource is missing": {
			cachedResourceMissing:      true,
			errorReason:                cachev1alpha1.CachedResourceNotFoundReason,
			wantCachedResourceNotValid: true,
			expectedCachedResourcePath: workspacePath,
		},
		"CachedResourceValid set to false if an internal error happens when fetching the CachedResource": {
			cachedResourceInternalErr:  true,
			wantError:                  true,
			errorReason:                cachev1alpha1.InternalErrorReason,
			wantCachedResourceNotValid: true,
			expectedCachedResourcePath: workspacePath,
		},
		"PartitionValid set to false when the Partition is missing": {
			partitionMissing:           true,
			errorReason:                cachev1alpha1.PartitionInvalidReferenceReason,
			wantPartitionNotValid:      true,
			expectedCachedResourcePath: workspacePath,
		},
		"CachedResource lookup uses referenced path when provided": {
			sliceMutator: func(slice *cachev1alpha1.CachedResourceEndpointSlice) {
				slice.Spec.CachedResource.Path = "root:org:external"
			},
			expectedCachedResourcePath: "root:org:external",
			wantCachedResourceValid:    true,
			wantPartitionValid:         true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gotCachedResourcePath string
			c := &controller{
				getCachedResource: func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error) {
					gotCachedResourcePath = path.String()
					if tc.cachedResourceMissing {
						return nil, apierrors.NewNotFound(cachev1alpha1.Resource("CachedResource"), name)
					} else if tc.cachedResourceInternalErr {
						return nil, fmt.Errorf("internal error")
					} else {
						return &cachev1alpha1.CachedResource{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-cr",
							},
						}, nil
					}
				},
				getPartition: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error) {
					if tc.partitionMissing {
						return nil, apierrors.NewNotFound(topologyv1alpha1.Resource("Partition"), name)
					} else {
						return &topologyv1alpha1.Partition{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-partition",
							},
							Spec: topologyv1alpha1.PartitionSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"region": "Europe",
									},
								},
							},
						}, nil
					}
				},
			}

			cachedResourceEndpointSlice := &cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: workspacePath,
					},
					Name: "my-slice",
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cr",
					},
					Partition: "my-partition",
				},
			}
			if tc.sliceMutator != nil {
				tc.sliceMutator(cachedResourceEndpointSlice)
			}
			err := c.reconcile(context.Background(), cachedResourceEndpointSlice)
			if tc.wantError {
				require.Error(t, err, "expected an error")
			} else {
				require.NoError(t, err, "expected no error")
			}

			if tc.wantCachedResourceNotValid {
				requireConditionMatches(t, cachedResourceEndpointSlice,
					conditions.FalseCondition(
						cachev1alpha1.CachedResourceValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantPartitionNotValid {
				requireConditionMatches(t, cachedResourceEndpointSlice,
					conditions.FalseCondition(
						cachev1alpha1.PartitionValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantCachedResourceValid {
				requireConditionMatches(t, cachedResourceEndpointSlice,
					conditions.TrueCondition(cachev1alpha1.CachedResourceValid),
				)
			}

			if tc.wantPartitionValid {
				requireConditionMatches(t, cachedResourceEndpointSlice,
					conditions.TrueCondition(cachev1alpha1.PartitionValid),
				)
			}

			if tc.expectedCachedResourcePath != "" {
				require.Equal(t, tc.expectedCachedResourcePath, gotCachedResourcePath, "unexpected cached resource lookup path")
			}
		})
	}
}

// requireConditionMatches looks for a condition matching c in g. LastTransitionTime and Message
// are not compared.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	t.Helper()
	actual := conditions.Get(g, c.Type)
	require.NotNil(t, actual, "missing condition %q", c.Type)
	actual.LastTransitionTime = c.LastTransitionTime
	actual.Message = c.Message
	require.Empty(t, cmp.Diff(actual, c))
}
