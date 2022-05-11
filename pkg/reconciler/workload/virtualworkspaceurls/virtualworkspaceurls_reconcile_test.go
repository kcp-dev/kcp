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

package virtualworkspaceurls

import (
	"reflect"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestReconciler(t *testing.T) {
	tests := map[string]struct {
		workspaceShards         []*workspaceapi.ClusterWorkspaceShard
		workloadCluster         *workloadv1alpha1.WorkloadCluster
		expectedWorkloadCluster *workloadv1alpha1.WorkloadCluster
		expectError             bool
	}{
		"WorkloadCluster with empty VirtualWorkspaces and one workspaceShards": {
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
			workloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{
						{
							URL: "http://external-host/services/syncer/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectError: false,
		},
		"WorkloadCluster and multiple ClusterWorkspaceShards": {
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
			workloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
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
		"WorkloadCluster with multiple ClusterWorkspaceShards with duplicated ExternalURLs results in a deduplicated list of URLs on the WorkloadCluster": {
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
			workloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
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
		"WorkloadCluster but no ClusterWorkspaceShards": {
			workspaceShards: []*workspaceapi.ClusterWorkspaceShard{},
			workloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
					VirtualWorkspaces: []workloadv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{},
			},
			expectError: false,
		},
		"WorkloadCluster from three to one ClusterWorkspaceShards": {
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
			workloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
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
			expectedWorkloadCluster: &workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-cluster",
					ClusterName: "demo:root:yourworkspace",
				},
				Spec: workloadv1alpha1.WorkloadClusterSpec{
					Unschedulable: false,
					EvictAfter:    nil,
				},
				Status: workloadv1alpha1.WorkloadClusterStatus{
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
			returnedWorkloadCluster, err := c.reconcile(tc.workloadCluster, tc.workspaceShards)
			if err != nil && tc.expectError != true {
				t.Errorf("unexpected error: %v", err)
			}
			sort.Slice(tc.expectedWorkloadCluster.Status.VirtualWorkspaces, func(i, j int) bool {
				return tc.expectedWorkloadCluster.Status.VirtualWorkspaces[i].URL < tc.expectedWorkloadCluster.Status.VirtualWorkspaces[j].URL
			})
			if !reflect.DeepEqual(returnedWorkloadCluster, tc.expectedWorkloadCluster) {
				t.Errorf("expected: %v, got: %v", tc.expectedWorkloadCluster, returnedWorkloadCluster)
			}
		})
	}
}
