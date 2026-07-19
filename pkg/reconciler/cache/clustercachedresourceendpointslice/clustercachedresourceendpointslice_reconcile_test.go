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

package clustercachedresourceendpointslice

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
)

func TestReconcile(t *testing.T) {
	t.Parallel()
	const workspacePath = "root:org:ws"
	tests := map[string]struct {
		keyMissing                       bool
		clusterCachedResourceMissing     bool
		partitionMissing                 bool
		clusterCachedResourceInternalErr bool
		listShardsError                  error
		errorReason                      string

		wantError                         bool
		wantVerifyFailure                 bool
		wantClusterCachedResourceValid    bool
		wantPartitionValid                bool
		wantClusterCachedResourceNotValid bool
		wantPartitionNotValid             bool

		sliceMutator                      func(slice *cachev1alpha1.ClusterCachedResourceEndpointSlice)
		expectedClusterCachedResourcePath string
	}{
		"ClusterCachedResourceValid set to false when ClusterCachedResource is missing": {
			clusterCachedResourceMissing:      true,
			errorReason:                       cachev1alpha1.ClusterCachedResourceNotFoundReason,
			wantClusterCachedResourceNotValid: true,
			expectedClusterCachedResourcePath: workspacePath,
		},
		"ClusterCachedResourceValid set to false if an internal error happens when fetching the ClusterCachedResource": {
			clusterCachedResourceInternalErr:  true,
			wantError:                         true,
			errorReason:                       cachev1alpha1.InternalErrorReason,
			wantClusterCachedResourceNotValid: true,
			expectedClusterCachedResourcePath: workspacePath,
		},
		"PartitionValid set to false when the Partition is missing": {
			partitionMissing:                  true,
			errorReason:                       cachev1alpha1.PartitionInvalidReferenceReason,
			wantPartitionNotValid:             true,
			expectedClusterCachedResourcePath: workspacePath,
		},
		"ClusterCachedResource lookup uses referenced path when provided": {
			sliceMutator: func(slice *cachev1alpha1.ClusterCachedResourceEndpointSlice) {
				slice.Spec.ClusterCachedResource.Path = "root:org:external"
			},
			expectedClusterCachedResourcePath: "root:org:external",
			wantClusterCachedResourceValid:    true,
			wantPartitionValid:                true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var gotClusterCachedResourcePath string
			c := &controller{
				getClusterCachedResource: func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error) {
					gotClusterCachedResourcePath = path.String()
					if tc.clusterCachedResourceMissing {
						return nil, apierrors.NewNotFound(cachev1alpha1.Resource("ClusterCachedResource"), name)
					} else if tc.clusterCachedResourceInternalErr {
						return nil, fmt.Errorf("internal error")
					} else {
						return &cachev1alpha1.ClusterCachedResource{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-cr",
							},
						}, nil
					}
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					apiGroup := "cache.kcp.io"
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: workspacePath,
							},
							Name: "my-export",
						},
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: ptr.To(apiGroup),
												Kind:     "ClusterCachedResourceEndpointSlice",
												Name:     "my-slice",
											},
										},
									},
								},
							},
						},
					}, nil
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

			clusterCachedResourceEndpointSlice := &cachev1alpha1.ClusterCachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: workspacePath,
					},
					Name: "my-slice",
				},
				Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
					ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{
						Name: "my-cr",
					},
					APIExport: cachev1alpha1.ExportBindingReference{
						Name: "my-export",
					},
					Partition: "my-partition",
				},
			}
			if tc.sliceMutator != nil {
				tc.sliceMutator(clusterCachedResourceEndpointSlice)
			}
			err := c.reconcile(context.Background(), clusterCachedResourceEndpointSlice)
			if tc.wantError {
				require.Error(t, err, "expected an error")
			} else {
				require.NoError(t, err, "expected no error")
			}

			if tc.wantClusterCachedResourceNotValid {
				requireConditionMatches(t, clusterCachedResourceEndpointSlice,
					conditions.FalseCondition(
						cachev1alpha1.ClusterCachedResourceValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantPartitionNotValid {
				requireConditionMatches(t, clusterCachedResourceEndpointSlice,
					conditions.FalseCondition(
						cachev1alpha1.PartitionValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantClusterCachedResourceValid {
				requireConditionMatches(t, clusterCachedResourceEndpointSlice,
					conditions.TrueCondition(cachev1alpha1.ClusterCachedResourceValid),
				)
			}

			if tc.wantPartitionValid {
				requireConditionMatches(t, clusterCachedResourceEndpointSlice,
					conditions.TrueCondition(cachev1alpha1.PartitionValid),
				)
			}

			if tc.expectedClusterCachedResourcePath != "" {
				require.Equal(t, tc.expectedClusterCachedResourcePath, gotClusterCachedResourcePath, "unexpected cached resource lookup path")
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
