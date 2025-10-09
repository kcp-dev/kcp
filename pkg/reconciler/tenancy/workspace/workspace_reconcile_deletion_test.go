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

package workspace

import (
	"context"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

type fakeDeletionReconciler struct {
	clusters map[string]*corev1alpha1.LogicalCluster
	deletionReconciler
}

func (f *fakeDeletionReconciler) getLogicalCluster() func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
	return func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
		c, ok := f.clusters[cluster.String()]
		if !ok {
			return nil, apierrors.NewNotFound(schema.GroupResource{}, cluster.String())
		}
		return c, nil
	}
}

func (f *fakeDeletionReconciler) deleteLogicalCluster() func(ctx context.Context, cluster logicalcluster.Path) error {
	return func(ctx context.Context, cluster logicalcluster.Path) error {
		delete(f.clusters, cluster.String())
		return nil
	}
}

func (f *fakeDeletionReconciler) getShardByHash() func(hash string) (*corev1alpha1.Shard, error) {
	return func(hash string) (*corev1alpha1.Shard, error) {
		// for now we can work with a fixed shard
		s := &corev1alpha1.Shard{}
		return s, nil
	}
}

func (f *fakeDeletionReconciler) kcpLogicalClusterAdminClientFor() func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error) {
	return func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
}

func TestReconcile(t *testing.T) {
	tt := []struct {
		name               string
		expStatus          reconcileStatus
		workspace          *tenancyv1alpha1.Workspace
		logicalclusters    map[string]*corev1alpha1.LogicalCluster
		expFinalizers      []string
		expLogicalClusters map[string]*corev1alpha1.LogicalCluster
	}{
		{
			name:      "no deletion timestamp",
			expStatus: reconcileStatusContinue,
			workspace: &tenancyv1alpha1.Workspace{},
		},
		{
			name:      "another finalizer is still set",
			expStatus: reconcileStatusContinue,
			workspace: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Finalizers: []string{
						corev1alpha1.LogicalClusterFinalizerName,
						"other-finalizer",
					},
				},
			},
			expFinalizers: []string{
				corev1alpha1.LogicalClusterFinalizerName,
				"other-finalizer",
			},
		},
		{
			name:      "workspace is still initializing, no logicalcluster exists",
			expStatus: reconcileStatusContinue,
			workspace: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Finalizers: []string{
						corev1alpha1.LogicalClusterFinalizerName,
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			},
			// we expect no finalizers here, as we can take a shortcut and directly
			// delete the workspace without needing to delete the logicalcluster first
			expFinalizers: []string{},
		},
		{
			name:      "workspace is still initializing, logicalcluster not marked for deletion yet",
			expStatus: reconcileStatusContinue,
			workspace: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Finalizers: []string{
						corev1alpha1.LogicalClusterFinalizerName,
					},
					Annotations: map[string]string{
						workspaceClusterAnnotationKey: "test",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			},
			logicalclusters: map[string]*corev1alpha1.LogicalCluster{
				"test": {},
			},
			// we do not remove our finalizers in this case, as we wait
			// for another reconciliation loop
			expFinalizers: []string{
				corev1alpha1.LogicalClusterFinalizerName,
			},
			// we do expect our logicalCluster to be removed
			expLogicalClusters: map[string]*corev1alpha1.LogicalCluster{},
		},
		{
			name:      "workspace is marked for deletion, logicalcluster is already deleted",
			expStatus: reconcileStatusStopAndRequeue,
			workspace: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
					Finalizers: []string{
						corev1alpha1.LogicalClusterFinalizerName,
					},
				},
			},
			// finalizer should be removed
			expFinalizers: []string{},
		},
		{
			name:      "workspace is marked for deletion, finalizer is already removed (edge case)",
			expStatus: reconcileStatusContinue,
			workspace: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
				},
			},
		},
	}

	// swallow any log output, so we don't pollute test results
	ctx := logr.NewContext(context.Background(), logr.Discard())

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			fdr := fakeDeletionReconciler{
				clusters: tc.logicalclusters,
			}
			fdr.deletionReconciler.getLogicalCluster = fdr.getLogicalCluster()
			fdr.deletionReconciler.deleteLogicalCluster = fdr.deleteLogicalCluster()
			fdr.deletionReconciler.getShardByHash = fdr.getShardByHash()
			fdr.deletionReconciler.kcpLogicalClusterAdminClientFor = fdr.kcpLogicalClusterAdminClientFor()

			status, err := fdr.deletionReconciler.reconcile(ctx, tc.workspace)

			if status != tc.expStatus {
				t.Errorf("Exp status %v, got %v", tc.expStatus, status)
			}

			if !slices.Equal(tc.expFinalizers, tc.workspace.Finalizers) {
				t.Errorf("Exp finalizers %v, got %v", tc.expFinalizers, tc.workspace.Finalizers)
			}

			if diff := cmp.Diff(tc.expLogicalClusters, tc.logicalclusters); diff != "" {
				t.Errorf("logicalcluster mismatch: %s", diff)
			}

			if err != nil {
				t.Errorf("did not expect an error, but got %b", err)
			}
		})
	}
}
