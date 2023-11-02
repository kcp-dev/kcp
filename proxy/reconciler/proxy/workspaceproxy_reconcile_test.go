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

package workspaceproxy

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestReconciler(t *testing.T) {
	tests := map[string]struct {
		workspaceShards        []*corev1alpha1.Shard
		workspaceProxy         *proxyv1alpha1.WorkspaceProxy
		expectedWorkspaceProxy *proxyv1alpha1.WorkspaceProxy
		expectError            bool
	}{
		"WorkspaceProxy with empty VirtualWorkspaces and one workspaceShards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace/",
					},
				},
			},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
					TunnelWorkspaces: []proxyv1alpha1.TunnelWorkspace{
						{
							URL: "http://1.2.3.4:6443/clusters/demo:root:yourworkspace",
						},
					},
				},
			},
			expectError: false,
		},
		"WorkspaceProxy and multiple Shards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6444/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-2/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6445/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-3/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-1/",
					},
				},
			},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace-1/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-2/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-3/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
					TunnelWorkspaces: []proxyv1alpha1.TunnelWorkspace{
						{
							URL: "http://1.2.3.4:6443/clusters/demo:root:yourworkspace",
						},
						{
							URL: "http://1.2.3.4:6444/clusters/demo:root:yourworkspace",
						},
						{
							URL: "http://1.2.3.4:6445/clusters/demo:root:yourworkspace",
						},
					},
				},
			},
			expectError: false,
		},
		"WorkspaceProxy and multiple Shards, but root shard always first": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6444/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-2/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6445/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-3/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-10/",
					},
				},
			},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace-10/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-2/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-3/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
					TunnelWorkspaces: []proxyv1alpha1.TunnelWorkspace{
						{
							URL: "http://1.2.3.4:6443/clusters/demo:root:yourworkspace",
						},
						{
							URL: "http://1.2.3.4:6444/clusters/demo:root:yourworkspace",
						},
						{
							URL: "http://1.2.3.4:6445/clusters/demo:root:yourworkspace",
						},
					},
				},
			},
			expectError: false,
		},
		"WorkspaceProxy with multiple Shards with duplicated VirtualWorkspaceURLs results in a deduplicated list of URLs on the WorkspaceProxy": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-1/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root2",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-1/",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root3",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6445/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-3/",
					},
				},
			},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{},
				},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace-1/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},

						{
							SyncerURL: "http://virtualworkspace-3/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
					TunnelWorkspaces: []proxyv1alpha1.TunnelWorkspace{
						{URL: "http://1.2.3.4:6443/clusters/demo:root:yourworkspace"},
						{URL: "http://1.2.3.4:6445/clusters/demo:root:yourworkspace"},
					},
				},
			},
			expectError: false,
		},
		"workspaceProxy but no Shards": {
			workspaceShards: []*corev1alpha1.Shard{},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec:   proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec:   proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{},
			},
			expectError: false,
		},
		"workspaceProxy from three to one Shards": {
			workspaceShards: []*corev1alpha1.Shard{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root",
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:             "http://1.2.3.4:6443/",
						ExternalURL:         "http://external-host/",
						VirtualWorkspaceURL: "http://virtualworkspace-1/",
					},
				},
			},
			workspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace-1/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-2/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
						{
							SyncerURL: "http://virtualworkspace-3/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
				},
			},
			expectedWorkspaceProxy: &proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "demo:root:yourworkspace",
					},
					Labels: map[string]string{
						"internal.proxy.kcp.io/key": "aPXkBdRsTD8gXESO47r9qXmkr2kaG5qaox5C8r",
					},
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{},
				Status: proxyv1alpha1.WorkspaceProxyStatus{
					VirtualWorkspaces: []proxyv1alpha1.VirtualWorkspace{
						{
							SyncerURL: "http://virtualworkspace-1/services/cluster-proxy/demo:root:yourworkspace/test-cluster",
						},
					},
					TunnelWorkspaces: []proxyv1alpha1.TunnelWorkspace{
						{
							URL: "http://1.2.3.4:6443/clusters/demo:root:yourworkspace",
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
			returnedWorkspaceProxy, err := c.reconcile(context.TODO(), tc.workspaceProxy, tc.workspaceShards)
			if err != nil && !tc.expectError {
				t.Errorf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(returnedWorkspaceProxy, tc.expectedWorkspaceProxy) {
				t.Errorf("expected diff: %s", cmp.Diff(tc.expectedWorkspaceProxy, returnedWorkspaceProxy))
			}
		})
	}
}
