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

package deployment

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/tmc/pkg/coordination"
)

func intPtr(i int32) *int32 {
	res := i
	return &res
}

func TestUpstreamViewReconciler(t *testing.T) {
	tests := map[string]struct {
		input, output *appsv1.Deployment
		wantError     bool
	}{
		"spread requested replicas": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			output: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": `[{ "op": "replace", "path": "/replicas", "value": 4 }]`,
						"experimental.spec-diff.workload.kcp.dev/syncTarget2": `[{ "op": "replace", "path": "/replicas", "value": 3 }]`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
		},
		"don't take empty labels into account": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			output: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": `[{ "op": "replace", "path": "/replicas", "value": 7 }]`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {

			var output *appsv1.Deployment
			controller := controller{
				getDeployment: func(lclusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error) {
					return tc.input, nil
				},
				updateDeployment: func(ctx context.Context, clusterName logicalcluster.Name, namespace string, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
					output = deployment
					return deployment, nil
				},
				updateDeploymentStatus: func(ctx context.Context, clusterName logicalcluster.Name, namespace string, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
					output = deployment
					return deployment, nil
				},
				syncerViewRetriever: coordination.NewDefaultSyncerViewManager[*appsv1.Deployment](),
			}

			err := controller.processUpstreamView(context.Background(), "")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.output, output)
		})
	}
}

func TestSyncerViewReconciler(t *testing.T) {
	tests := map[string]struct {
		input, output *appsv1.Deployment
		wantError     bool
	}{
		"summarize available replicas": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `{ "status": { "availableReplicas": 4 }}`,
						"diff.syncer.internal.kcp.dev/syncTarget2": `{ "status": { "availableReplicas": 3 }}`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			output: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `{ "status": { "availableReplicas": 4 }}`,
						"diff.syncer.internal.kcp.dev/syncTarget2": `{ "status": { "availableReplicas": 3 }}`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
				Status: appsv1.DeploymentStatus{
					AvailableReplicas: 7,
				},
			},
		},
		"summarize conditions": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `{ "status": { "conditions": [ { "type": "Available", "status": "True" }, {"type": "ReplicaFailure", "status": "Unknown" }, { "type": "Progressing", "status": "False" } ] } }`,
						"diff.syncer.internal.kcp.dev/syncTarget2": `{ "status": { "conditions": [ { "type": "ReplicaFailure", "status": "False" }, { "type": "Progressing", "status": "True" } ] } }`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			output: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `{ "status": { "conditions": [ { "type": "Available", "status": "True" }, {"type": "ReplicaFailure", "status": "Unknown" }, { "type": "Progressing", "status": "False" } ] } }`,
						"diff.syncer.internal.kcp.dev/syncTarget2": `{ "status": { "conditions": [ { "type": "ReplicaFailure", "status": "False" }, { "type": "Progressing", "status": "True" } ] } }`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   appsv1.DeploymentReplicaFailure,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {

			var updateStatusOutput *appsv1.Deployment
			var updateOutput *appsv1.Deployment
			controller := controller{
				getDeployment: func(lclusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error) {
					return tc.input, nil
				},
				updateDeployment: func(ctx context.Context, clusterName logicalcluster.Name, namespace string, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
					updateOutput = deployment.DeepCopy()
					return deployment, nil
				},
				updateDeploymentStatus: func(ctx context.Context, clusterName logicalcluster.Name, namespace string, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
					updateStatusOutput = deployment.DeepCopy()
					return deployment, nil
				},
				syncerViewRetriever: coordination.NewDefaultSyncerViewManager[*appsv1.Deployment](),
			}

			err := controller.processSyncerView(context.Background(), "")
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.output, updateStatusOutput)
			require.Nil(t, updateOutput)
		})
	}
}
