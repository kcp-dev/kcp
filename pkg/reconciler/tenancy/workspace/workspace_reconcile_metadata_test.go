/*
Copyright 2022 The kcp Authors.

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

package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1helper "github.com/kcp-dev/sdk/apis/core/v1alpha1/helper"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestReconcileMetadata(t *testing.T) {
	date, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	require.NoError(t, err)

	for _, testCase := range []struct {
		name              string
		input             *tenancyv1alpha1.Workspace
		getLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
		getShardByHash    func(hash string) (*corev1alpha1.Shard, error)
		expected          metav1.ObjectMeta
		expectedSpec      tenancyv1alpha1.WorkspaceSpec
		wantStatus        reconcileStatus
		wantErr           bool
	}{
		{
			name: "removes everything but owner username when ready",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
					},
					Annotations: map[string]string{
						"a":                                 "b",
						"experimental.tenancy.kcp.io/owner": `{"username":"user-1","groups":["a","b"],"uid":"123","extra":{"c":["d"]}}`,
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
				},
				Annotations: map[string]string{
					"a":                                 "b",
					"experimental.tenancy.kcp.io/owner": `{"username":"user-1"}`,
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "label tracks Terminating phase",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: date},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseTerminating,
				},
			},
			expected: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{Time: date},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": string(corev1alpha1.LogicalClusterPhaseTerminating),
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "label tracks Deleting phase",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: date},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseDeleting,
				},
			},
			expected: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{Time: date},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": string(corev1alpha1.LogicalClusterPhaseDeleting),
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "delete invalid owner annotation when ready",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
					},
					Annotations: map[string]string{
						"a":                                 "b",
						"experimental.tenancy.kcp.io/owner": `{"username":}`,
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
				},
				Annotations: map[string]string{
					"a": "b",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "drift: LogicalCluster shard differs from workspace, restamps annotations",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "old-hash",
						WorkspaceShardHashAnnotationKey:               "old-hash",
					},
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
						"region":               "us-east-1",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "test-cluster",
					URL:     "https://old.example.com/clusters/root:ws",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			getLogicalCluster: func(_ context.Context, _ logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							corev1alpha1.LogicalClusterShardAnnotationKey: corev1alpha1helper.ShardNameHash("new-shard"),
						},
					},
				}, nil
			},
			getShardByHash: func(hash string) (*corev1alpha1.Shard, error) {
				return &corev1alpha1.Shard{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "new-shard",
						Labels: map[string]string{"region": "us-west-2"},
					},
				}, nil
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: corev1alpha1helper.ShardNameHash("new-shard"),
					WorkspaceShardHashAnnotationKey:               corev1alpha1helper.ShardNameHash("new-shard"),
				},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
					"region":               "us-west-2",
				},
			},
			expectedSpec: tenancyv1alpha1.WorkspaceSpec{
				Cluster: "test-cluster",
				URL:     "https://old.example.com/clusters/root:ws",
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "no drift: LogicalCluster shard matches workspace shard",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
						WorkspaceShardHashAnnotationKey:               "hash-1",
					},
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "test-cluster",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			getLogicalCluster: func(_ context.Context, _ logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
						},
					},
				}, nil
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
					WorkspaceShardHashAnnotationKey:               "hash-1",
				},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
				},
			},
			expectedSpec: tenancyv1alpha1.WorkspaceSpec{
				Cluster: "test-cluster",
			},
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "LogicalCluster has no shard annotation yet: requeue until stamped",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
						WorkspaceShardHashAnnotationKey:               "hash-1",
					},
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "test-cluster",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			getLogicalCluster: func(_ context.Context, _ logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{}, nil
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
					WorkspaceShardHashAnnotationKey:               "hash-1",
				},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
				},
			},
			expectedSpec: tenancyv1alpha1.WorkspaceSpec{
				Cluster: "test-cluster",
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "LogicalCluster not found: requeue with error",
			input: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
						WorkspaceShardHashAnnotationKey:               "hash-1",
					},
					Labels: map[string]string{
						"tenancy.kcp.io/phase": "Ready",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "test-cluster",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			getLogicalCluster: func(_ context.Context, _ logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), "cluster")
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: "hash-1",
					WorkspaceShardHashAnnotationKey:               "hash-1",
				},
				Labels: map[string]string{
					"tenancy.kcp.io/phase": "Ready",
				},
			},
			expectedSpec: tenancyv1alpha1.WorkspaceSpec{
				Cluster: "test-cluster",
			},
			wantStatus: reconcileStatusStopAndRequeue,
			wantErr:    true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reconciler := metaDataReconciler{
				getLogicalCluster: testCase.getLogicalCluster,
				getShardByHash:    testCase.getShardByHash,
			}
			status, err := reconciler.reconcile(context.Background(), testCase.input)

			if testCase.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, testCase.wantStatus, status)

			if diff := cmp.Diff(testCase.input.ObjectMeta, testCase.expected); diff != "" {
				t.Errorf("invalid output after reconciling metadata: %v", diff)
			}
			if diff := cmp.Diff(testCase.input.Spec, testCase.expectedSpec); diff != "" {
				t.Errorf("invalid spec after reconciling metadata: %v", diff)
			}
		})
	}
}
