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

package apiexportendpointsliceurls

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	apisv1alpha1apply "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/apis/v1alpha1"
)

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		input               *apisv1alpha1.APIExportEndpointSlice
		endpointsReconciler *endpointsReconciler
		expectedConditions  []*conditionsv1alpha1.Condition
		expectedError       error
	}{
		"condition not ready": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{},
		},
		"empty selector": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Name: "my-export",
						Path: "root:org:ws",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return &apisv1alpha1.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-export",
						},
					}, nil
				},
			},
		},
		"invalid selector": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: ",",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{},
			expectedError:       errors.New("invalid selector: ,"),
		},
		"error getting apiExport": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return nil, errors.New("lost in space")
				},
			},
			expectedError: errors.New("lost in space"),
		},
		"update endpoint - not my shard - no update": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				shardName: "shard2",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return &apisv1alpha1.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
					}, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if len(patch.Status.APIExportEndpoints) != 1 && patch.Status.APIExportEndpoints[0].URL != ptr.To("") {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, no consumers": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				shardName: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return &apisv1alpha1.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
					return nil, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if patch.Status.APIExportEndpoints != nil {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, consumer went away, remove url": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
					APIExportEndpoints: []apisv1alpha1.APIExportEndpoint{
						{
							URL: "https://server-1.kcp.dev/who-took-the-cookie-from-the-cookie-jar",
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				shardName: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return &apisv1alpha1.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
					return nil, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if patch.Status.APIExportEndpoints != nil {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, consumer exists, add url": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha1.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				shardName: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					return &apisv1alpha1.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-export",
						},
					}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
					return []*apisv1alpha1.APIBinding{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "my-binding",
							},
						},
					}, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if len(patch.Status.APIExportEndpoints) != 1 {
						t.Fatalf("unexpected update: %v", patch)
					}
					url := ptr.Deref(patch.Status.APIExportEndpoints[0].URL, "")
					if url != "https://server-1.kcp.dev/services/apiexport/my-export" {
						t.Fatalf("unexpected update: %v", patch)
					}
					return nil
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := &controller{
				getMyShard:                  tc.endpointsReconciler.getMyShard,
				getAPIExport:                tc.endpointsReconciler.getAPIExport,
				listAPIBindingsByAPIExport:  tc.endpointsReconciler.listAPIBindingsByAPIExport,
				patchAPIExportEndpointSlice: tc.endpointsReconciler.patchAPIExportEndpointSlice,
				shardName:                   tc.endpointsReconciler.shardName,
			}
			input := tc.input.DeepCopy()
			_, err := c.reconcile(context.Background(), input)
			if tc.expectedError != nil {
				require.Error(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err, "expected no error")
			}

			for _, expectedCondition := range tc.expectedConditions {
				requireConditionMatches(t, input, expectedCondition)
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
