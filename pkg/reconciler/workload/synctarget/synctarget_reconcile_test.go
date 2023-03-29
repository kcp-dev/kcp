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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

func TestReconciler(t *testing.T) {
	tests := map[string]struct {
		workspaceShards    []*corev1alpha1.Shard
		syncTarget         *workloadv1alpha1.SyncTarget
		expectedSyncTarget *workloadv1alpha1.SyncTarget
		expectError        bool
	}{
		"SyncTarget with empty VirtualWorkspaces and one workspaceShards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host/",
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							SyncerURL:   "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget and multiple Shards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-2/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-3/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-1/",
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							SyncerURL:   "http://external-host-1/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-1/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-2/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-2/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-3/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget and multiple Shards, but root shard always first": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-2/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-3/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-10/",
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							SyncerURL:   "http://external-host-10/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-10/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-2/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-2/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-3/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget with multiple Shards with duplicated VirtualWorkspaceURLs results in a deduplicated list of URLs on the SyncTarget": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-1/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-1/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-3/",
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							SyncerURL:   "http://external-host-1/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-1/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},

						{
							SyncerURL:   "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-3/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"SyncTarget but no Shards": {
			workspaceShards: []*corev1alpha1.Shard{},
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
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
		"SyncTarget from three to one Shards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4/",
						VirtualWorkspaceURL: "http://external-host-1/",
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
							SyncerURL:   "http://external-host-1/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-1/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-2/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-2/services/upsyncer/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL:   "http://external-host-3/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-3/services/upsyncer/demo:root:yourworkspace/test-cluster",
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
						"internal.workload.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.SyncTargetStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							SyncerURL:   "http://external-host-1/services/syncer/demo:root:yourworkspace/test-cluster",
							UpsyncerURL: "http://external-host-1/services/upsyncer/demo:root:yourworkspace/test-cluster",
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
			if err != nil && !tc.expectError {
				t.Errorf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(returnedSyncTarget, tc.expectedSyncTarget) {
				t.Errorf("expected diff: %s", cmp.Diff(tc.expectedSyncTarget, returnedSyncTarget))
			}
		})
	}
}
