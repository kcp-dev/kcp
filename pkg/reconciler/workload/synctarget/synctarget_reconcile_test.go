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

package synctarget

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestReconciler(t *testing.T) {
	tests := map[string]struct {
		workspaceShards    []*workspaceapi.ClusterWorkspaceShard
		syncTarget         *workloadv1alpha1.SyncTarget
		expectedSyncTarget *workloadv1alpha1.SyncTarget
		expectError        bool
	}{
		"SyncTarget with empty VirtualWorkspaces and one workspaceShards": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host/",
					},
				},
			},
			syncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedSyncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.workload.kcp.dev/key": "2Fhhz9cq06pipXqhKzp8wrxSgTVTUzc8fKKqLI",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget and multiple ClusterWorkspaceShards": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host-2/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host-3/",
					},
				},
			},
			syncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedSyncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.workload.kcp.dev/key": "2Fhhz9cq06pipXqhKzp8wrxSgTVTUzc8fKKqLI",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},
						{
							URL: "http://external-host-2/services/syncer/demo:root:yourworkspace/test-cluster",
						},
						{
							URL: "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget with multiple ClusterWorkspaceShards with duplicated ExternalURLs results in a deduplicated list of URLs on the SyncTarget": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host-3/",
					},
				},
			},
			syncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedSyncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.workload.kcp.dev/key": "2Fhhz9cq06pipXqhKzp8wrxSgTVTUzc8fKKqLI",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},

						{
							URL: "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget but no ClusterWorkspaceShards": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{},
			syncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{},
			},
			expectedSyncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.workload.kcp.dev/key": "2Fhhz9cq06pipXqhKzp8wrxSgTVTUzc8fKKqLI",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{},
			},
			expectError: false,
		},
		"SyncTarget from three to one ClusterWorkspaceShards": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: workspaceapi.ClusterWorkspaceShardSpec{
						BaseURL:     "http://1.2.3.4/",
						ExternalURL: "http://external-host/",
					},
				},
			},
			syncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},
						{
							URL: "http://external-host-2/services/syncer/demo:root:yourworkspace/test-cluster",
						},
						{
							URL: "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectedSyncTarget: &workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.workload.kcp.dev/key": "2Fhhz9cq06pipXqhKzp8wrxSgTVTUzc8fKKqLI",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := Controller{}
			returnedSyncTarget, err := c.reconcile(context.TODO(), tc.syncTarget, tc.workspaceShards)
			if err != nil && tc.expectError != true {
				t.Errorf("unexpected error: %v", err)
			}
			sort.Slice(tc.expectedSyncTarget.Status.VirtualWorkspaces, func(i, j int) bool {
				return tc.expectedSyncTarget.Status.VirtualWorkspaces[i].URL < tc.expectedSyncTarget.Status.VirtualWorkspaces[j].URL
			})
			if !reflect.DeepEqual(returnedSyncTarget, tc.expectedSyncTarget) {
				t.Errorf("expected: %v, got: %v", tc.expectedSyncTarget, returnedSyncTarget)
			}
		})
	}
}
