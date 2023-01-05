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

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		keyMissing             bool
		apiExportMissing       bool
		apiExportHasInvalidRef bool
		listShardsError        error
		errorReason            string

		wantError                           bool
		wantVerifyFailure                   bool
		wantAPIExportEndpointSliceURLsError bool
		wantAPIExportEndpointSliceURLsReady bool
		wantAPIExportValid                  bool
		wantAPIExportNotValid               bool
	}{
		"error listing shards": {
			listShardsError:                     errors.New("foo"),
			wantError:                           true,
			wantAPIExportEndpointSliceURLsError: true,
		},
		"APIExportValid set to false when apiExport is missing": {
			apiExportMissing:      true,
			errorReason:           apisv1alpha1.APIExportNotFoundReason,
			wantAPIExportNotValid: true,
		},
		"APIExportEndpointSliceURLs set when no issue": {
			wantAPIExportEndpointSliceURLsReady: true,
			wantAPIExportValid:                  true,
		},
	}

	for name, tc := range tests {
		tc := tc // to avoid t.Parallel() races

		t.Run(name, func(t *testing.T) {
			c := &controller{
				listShards: func() ([]*corev1alpha1.Shard, error) {
					if tc.listShardsError != nil {
						return nil, tc.listShardsError
					}

					return []*corev1alpha1.Shard{
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "shard1",
							},
							Spec: corev1alpha1.ShardSpec{
								ExternalURL: "https://server-1.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "shard2",
							},
							Spec: corev1alpha1.ShardSpec{
								ExternalURL: "https://server-2.kcp.dev/",
							},
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					if tc.apiExportMissing {
						return nil, apierrors.NewNotFound(apisv1alpha1.Resource("APIExport"), name)
					} else if tc.apiExportHasInvalidRef {
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

			if tc.wantAPIExportValid {
				requireConditionMatches(t, apiExportEndpointSlice,
					conditions.TrueCondition(apisv1alpha1.APIExportValid),
				)
			}
		})
	}
}

// requireConditionMatches looks for a condition matching c in g. LastTransitionTime and Message
// are not compared.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	actual := conditions.Get(g, c.Type)

	require.NotNil(t, actual, "missing condition %q", c.Type)
	actual.LastTransitionTime = c.LastTransitionTime
	actual.Message = c.Message
	require.Empty(t, cmp.Diff(actual, c))
}
