/*
Copyright 2024 The KCP Authors.

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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestReconcilePhase(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		input             *tenancyv1alpha1.Workspace
		getLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)

		wantPhase     corev1alpha1.LogicalClusterPhaseType
		wantStatus    reconcileStatus
		wantCondition conditionsv1alpha1.Condition
	}{
		{
			name: "workspace is scheduling but not yet initialized",
			input: &tenancyv1alpha1.Workspace{
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseScheduling,
			wantStatus: reconcileStatusContinue,
			wantCondition: conditionsv1alpha1.Condition{
				Type:   tenancyv1alpha1.WorkspaceInitialized,
				Status: corev1.ConditionFalse,
			},
		},
		{
			name: "workspace is scheduled and moved to initializing",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseInitializing,
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "workspace is initializing and logical cluster is not found",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalcluster"), "cluster-1")
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseInitializing,
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "workspace is initializing and logicalCluster not yet initialized",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{
							"initializer-1",
						},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseInitializing,
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "workspace is initializing and logicalCluster initialized",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseReady,
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "workspace is ready - no-op",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseReady,
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "workspace is ready - but condition is not ready",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   tenancyv1alpha1.MountConditionReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseUnavailable,
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "workspace is no ready - but condition is ready",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseUnavailable,
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   tenancyv1alpha1.MountConditionReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseReady,
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "non blocking condition is not ready",
			input: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://example.com",
					Cluster: "cluster-1",
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseUnavailable,
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   conditionsv1alpha1.ConditionType("UpdateIsNeeded"),
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return &corev1alpha1.LogicalCluster{
					Status: corev1alpha1.LogicalClusterStatus{
						Initializers: []corev1alpha1.LogicalClusterInitializer{},
					},
				}, nil
			},
			wantPhase:  corev1alpha1.LogicalClusterPhaseReady,
			wantStatus: reconcileStatusStopAndRequeue,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reconciler := phaseReconciler{
				getLogicalCluster: testCase.getLogicalCluster,
				requeueAfter:      func(workspace *tenancyv1alpha1.Workspace, after time.Duration) {},
			}
			status, err := reconciler.reconcile(context.Background(), testCase.input)

			require.NoError(t, err)
			require.Equal(t, testCase.wantStatus, status)
			require.Equal(t, testCase.wantPhase, testCase.input.Status.Phase)

			for _, condition := range testCase.input.Status.Conditions {
				if condition.Type == testCase.wantCondition.Type {
					require.Equal(t, testCase.wantCondition, condition)
				}
			}
		})
	}
}
