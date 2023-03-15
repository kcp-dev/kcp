/*
Copyright 2022 The KCP Authors.

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

package apiexportendpointslice

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		keyMissing           bool
		apiExportMissing     bool
		partitionMissing     bool
		apiExportInternalErr bool
		listShardsError      error
		errorReason          string

		wantError                             bool
		wantVerifyFailure                     bool
		wantAPIExportEndpointSliceURLsError   bool
		wantAPIExportEndpointSliceURLsReady   bool
		wantAPIExportEndpointSliceURLsUnknown bool
		wantAPIExportValid                    bool
		wantPartitionValid                    bool
		wantAPIExportNotValid                 bool
		wantPartitionNotValid                 bool
	}{
		"error listing shards": {
			listShardsError:                     errors.New("foo"),
			wantError:                           true,
			wantAPIExportEndpointSliceURLsError: true,
		},
		"APIExportValid set to false when APIExport is missing": {
			apiExportMissing:      true,
			errorReason:           apisv1alpha1.APIExportNotFoundReason,
			wantAPIExportNotValid: true,
		},
		"APIExportValid set to false if an internal error happens when fetching the APIExport": {
			apiExportInternalErr:                  true,
			wantError:                             true,
			errorReason:                           apisv1alpha1.InternalErrorReason,
			wantAPIExportNotValid:                 true,
			wantAPIExportEndpointSliceURLsUnknown: true,
		},
		"PartitionValid set to false when the Partition is missing": {
			partitionMissing:                    true,
			errorReason:                         apisv1alpha1.PartitionInvalidReferenceReason,
			wantPartitionNotValid:               true,
			wantAPIExportEndpointSliceURLsError: true,
		},
		"APIExportEndpointSliceURLs set when no issue": {
			wantAPIExportEndpointSliceURLsReady: true,
			wantAPIExportValid:                  true,
			wantPartitionValid:                  true,
		},
	}

	for name, tc := range tests {
		tc := tc // to avoid t.Parallel() races

		t.Run(name, func(t *testing.T) {
			c := &controller{
				listShards: func(selector labels.Selector) ([]*corev1alpha1.Shard, error) {
					if tc.listShardsError != nil {
						return nil, tc.listShardsError
					}

					return []*corev1alpha1.Shard{
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
								},
								Name: "shard1",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-1.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
								},
								Name: "shard2",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-2.kcp.dev/",
							},
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					if tc.apiExportMissing {
						return nil, apierrors.NewNotFound(apisv1alpha1.Resource("APIExport"), name)
					} else if tc.apiExportInternalErr {
						return nil, fmt.Errorf("internal error")
					} else {
						return &apisv1alpha1.APIExport{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-export",
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

			apiExportEndpointSlice := &apisv1alpha1.APIExportEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:ws",
					},
					Name: "my-slice",
				},
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
					Partition: "my-partition",
				},
			}
			err := c.reconcile(context.Background(), apiExportEndpointSlice)
			if tc.wantError {
				require.Error(t, err, "expected an error")
			} else {
				require.NoError(t, err, "expected no error")
			}

			if tc.wantAPIExportEndpointSliceURLsError {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.FalseCondition(
						apisv1alpha1.APIExportEndpointSliceURLsReady,
						apisv1alpha1.ErrorGeneratingURLsReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantAPIExportEndpointSliceURLsReady {
				requireConditionMatches(t, apiExportEndpointSlice, conditions.TrueCondition(apisv1alpha1.APIExportEndpointSliceURLsReady))
				require.Equal(t, []apisv1alpha1.APIExportEndpoint{
					{URL: "https://server-1.kcp.dev/services/apiexport/root:org:ws/my-export"},
					{URL: "https://server-2.kcp.dev/services/apiexport/root:org:ws/my-export"},
				}, apiExportEndpointSlice.Status.APIExportEndpoints)
			}

			if tc.wantAPIExportEndpointSliceURLsUnknown {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.UnknownCondition(
						apisv1alpha1.APIExportEndpointSliceURLsReady,
						apisv1alpha1.ErrorGeneratingURLsReason,
						"",
					),
				)
			}

			if tc.wantAPIExportNotValid {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.FalseCondition(
						apisv1alpha1.APIExportValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantPartitionNotValid {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.FalseCondition(
						apisv1alpha1.PartitionValid,
						tc.errorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantAPIExportValid {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.TrueCondition(apisv1alpha1.APIExportValid),
				)
			}

			if tc.wantPartitionValid {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.TrueCondition(apisv1alpha1.PartitionValid),
				)
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
