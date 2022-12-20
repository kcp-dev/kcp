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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/tmc/pkg/coordination"
)

func intPtr(i int32) *int32 {
	res := i
	return &res
}

type mockedPatcher struct {
	clusterName  logicalcluster.Name
	namespace    string
	appliedPatch string
}

func (p *mockedPatcher) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*Deployment, error) {
	p.appliedPatch = string(data)
	return nil, nil
}

func TestUpstreamViewReconciler(t *testing.T) {
	tests := map[string]struct {
		input        *appsv1.Deployment
		appliedPatch string
		wantError    bool
	}{
		"spread requested replicas": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			appliedPatch: `{"metadata":{"annotations":{"experimental.spec-diff.workload.kcp.dev/syncTarget1":"[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 4 }]","experimental.spec-diff.workload.kcp.dev/syncTarget2":"[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 3 }]"},"resourceVersion":"resourceVersion","uid":"uid"}}`,
		},
		"remove obsolete spec-diff annotation": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
					},
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": "[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 3 }]",
						"experimental.spec-diff.workload.kcp.dev/syncTarget2": "[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 4 }]",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			appliedPatch: `{"metadata":{"annotations":{"experimental.spec-diff.workload.kcp.dev/syncTarget1":"[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 7 }]","experimental.spec-diff.workload.kcp.dev/syncTarget2":null},"resourceVersion":"resourceVersion","uid":"uid"}}`,
		},
		"don't take empty labels into account": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			appliedPatch: `{"metadata":{"annotations":{"experimental.spec-diff.workload.kcp.dev/syncTarget1":"[{ \"op\": \"replace\", \"path\": \"/replicas\", \"value\": 7 }]"},"resourceVersion":"resourceVersion","uid":"uid"}}`,
		},
		"Invalid deletion annotation": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
					},
					Annotations: map[string]string{
						"deletion.internal.workload.kcp.dev/syncTarget2": "wrong format",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			wantError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {

			var patcher *mockedPatcher
			controller := controller{
				getDeployment: func(lclusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error) {
					return tc.input, nil
				},
				patcher: func(clusterName logicalcluster.Name, namespace string) committer.Patcher[*appsv1.Deployment] {
					patcher = &mockedPatcher{
						clusterName: clusterName,
						namespace:   namespace,
					}
					return patcher
				},
				syncerViewRetriever: coordination.NewDefaultSyncerViewManager[*appsv1.Deployment](),
			}

			err := controller.processUpstreamView(context.Background(), "")
			if tc.wantError {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.appliedPatch, patcher.appliedPatch)
		})
	}
}

func TestSyncerViewReconciler(t *testing.T) {
	tests := map[string]struct {
		input        *appsv1.Deployment
		appliedPatch string
		wantError    bool
	}{
		"summarize available replicas": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
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
			appliedPatch: `{"metadata":{"resourceVersion":"resourceVersion","uid":"uid"},"status":{"availableReplicas":7}}`,
		},
		"summarize available replicas - one with empty status": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `{ "status": { "availableReplicas": 4 }}`,
						"diff.syncer.internal.kcp.dev/syncTarget2": `{ "status": { "availableReplicas": 3 }}`,
						"diff.syncer.internal.kcp.dev/syncTarget3": `{ "status": {}}`,
					},
					Labels: map[string]string{
						"state.workload.kcp.dev/syncTarget1": "Sync",
						"state.workload.kcp.dev/syncTarget2": "Sync",
						"state.workload.kcp.dev/syncTarget3": "Sync",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(7),
				},
			},
			appliedPatch: `{"metadata":{"resourceVersion":"resourceVersion","uid":"uid"},"status":{"availableReplicas":7}}`,
		},
		"summarize conditions": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
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
			appliedPatch: `{"metadata":{"resourceVersion":"resourceVersion","uid":"uid"},"status":{"conditions":[{"lastTransitionTime":null,"lastUpdateTime":null,"status":"True","type":"Available"},{"lastTransitionTime":null,"lastUpdateTime":null,"status":"True","type":"Progressing"},{"lastTransitionTime":null,"lastUpdateTime":null,"status":"False","type":"ReplicaFailure"}]}}`,
		},
		"invalid syncer views": {
			input: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					UID:             types.UID("uid"),
					ResourceVersion: "resourceVersion",
					Annotations: map[string]string{
						"diff.syncer.internal.kcp.dev/syncTarget1": `invalid json`,
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
			wantError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {

			var patcher *mockedPatcher
			controller := controller{
				getDeployment: func(lclusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error) {
					return tc.input, nil
				},
				patcher: func(clusterName logicalcluster.Name, namespace string) committer.Patcher[*appsv1.Deployment] {
					patcher = &mockedPatcher{
						clusterName: clusterName,
						namespace:   namespace,
					}
					return patcher
				},
				syncerViewRetriever: coordination.NewDefaultSyncerViewManager[*appsv1.Deployment](),
			}

			err := controller.processSyncerView(context.Background(), "")
			if tc.wantError {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.appliedPatch, patcher.appliedPatch)
		})
	}
}

func TestDeploymentContentsEqual(t *testing.T) {
	tests := map[string]struct {
		old, new *appsv1.Deployment
		result   bool
	}{
		"only spec-diff annotation differs": {
			old: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": "value 1",
					},
				},
			},
			new: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": "value 2",
					},
				},
			},
			result: true,
		},
		"annotations differs": {
			old: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": "value 1",
					},
				},
			},
			new: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"experimental.spec-diff.workload.kcp.dev/syncTarget1": "value 2",
						"anotherone": "othervalue",
					},
				},
			},
			result: false,
		},
		"labels differs": {
			old: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"myLabel1": "value 1",
					},
				},
			},
			new: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"myLabel2": "value 2",
					},
				},
			},
			result: false,
		},
		"labels equal": {
			old: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"myLabel": "value",
					},
				},
			},
			new: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"myLabel": "value",
					},
				},
			},
			result: true,
		},
		"both replicas are nil - other content differ": {
			old: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					MinReadySeconds: 3,
					Replicas:        nil,
				},
			},
			new: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					MinReadySeconds: 4,
					Replicas:        nil,
				},
			},
			result: true,
		},
		"only old replicas is nil": {
			old: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: nil,
				},
			},
			new: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(4),
				},
			},
			result: false,
		},
		"only new replicas is nil": {
			old: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(4),
				},
			},
			new: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: nil,
				},
			},
			result: false,
		},
		"non-nil replicas equal - other content differ": {
			old: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					MinReadySeconds: 3,
					Replicas:        intPtr(4),
				},
			},
			new: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					MinReadySeconds: 4,
					Replicas:        intPtr(4),
				},
			},
			result: true,
		},
		"non-nil replicas diff": {
			old: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(4),
				},
			},
			new: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: intPtr(5),
				},
			},
			result: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.result, deploymentContentsEqual(tc.old, tc.new))
		})
	}
}
