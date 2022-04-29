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

package placement

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestPlacementReconciler(t *testing.T) {
	tests := map[string]struct {
		apibindings      map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding
		locations        map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location
		workloadClusters map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster
		namespace        *corev1.Namespace

		listLocationsError        error
		listAPIBindingsError      error
		listWorkloadClustersError error
		patchNamespaceError       error

		wantPatch           string
		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no placement, no binding, no locations, no cluster workspaces": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"existing placement, no binding, no locations, no cluster workspaces": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{"foo": "bar"}`,
					},
				},
			},
			wantPatch:           `{"metadata":{"annotations":{"scheduling.kcp.dev/placement":null}}}}`,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no placement, with workload cluster, but no location": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				binding("kubernetes", "negotiation-workspace"),
			}},
			workloadClusters: map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster{logicalcluster.New("root:org:negotiation-workspace"): {
				cluster("cluster123", "uid-123"),
			}},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no placement, with location, no workload cluster": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "negotiation-workspace"))),
			}},
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1-az1"), map[string]string{"region": "us-east1"}),
			}},
			wantRequeue:         time.Second * 30,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no placement, with location, no workload cluster, not bound binding": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				validExport(binding("kubernetes", "negotiation-workspace")),
			}},
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1-az1"), map[string]string{"region": "us-east1"}),
			}},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no placement, with location, no workload cluster, invalid export": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(binding("kubernetes", "negotiation-workspace")),
			}},
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1-az1"), map[string]string{"region": "us-east1"}),
			}},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"happy case: binding, locations and ready workload cluster exist, annotation does not": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "negotiation-workspace"))),
			}},
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(cluster("us-east1-1", "uid-1"), map[string]string{"region": "us-east1"}),
					withLabels(withConditions(cluster("us-east1-2", "uid-2"), conditionsv1alpha1.Condition{Type: "Ready", Status: "False"}), map[string]string{"region": "us-east1"}),
					withLabels(withConditions(cluster("us-east1-3", "uid-3"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-east1"}),
					withLabels(unschedulable(withConditions(cluster("us-east1-4", "uid-4"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"})), map[string]string{"region": "us-east1"}),

					withLabels(withConditions(cluster("us-west1-1", "uid-11"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-west1"}),
					withLabels(withConditions(cluster("us-west1-2", "uid-12"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-west1"}),
				},
				logicalcluster.New("root:org:somewhere-else"): {
					cluster("us-east1-1", "uid-21"),
					cluster("us-east1-2", "uid-22"),
				},
			},
			wantPatch:           `{"metadata":{"annotations":{"scheduling.kcp.dev/placement":"{\"us-east1+uid-3\":\"Pending\"}"}}}`,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"patch fails": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.LogicalCluster][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "negotiation-workspace"))),
			}},
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(withConditions(cluster("us-east1-3", "uid-3"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-east1"}),
				},
			},
			patchNamespaceError: fmt.Errorf("boom"),
			wantError:           true,
			wantReconcileStatus: reconcileStatusStop,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			var gotPatch string
			r := &placementReconciler{
				listLocations: func(clusterName logicalcluster.LogicalCluster) ([]*schedulingv1alpha1.Location, error) {
					if tc.listLocationsError != nil {
						return nil, tc.listLocationsError
					}
					return tc.locations[clusterName], nil
				},
				listAPIBindings: func(clusterName logicalcluster.LogicalCluster) ([]*apisv1alpha1.APIBinding, error) {
					if tc.listLocationsError != nil {
						return nil, tc.listLocationsError
					}
					return tc.apibindings[clusterName], nil
				},
				listWorkloadClusters: func(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error) {
					if tc.listWorkloadClustersError != nil {
						return nil, tc.listWorkloadClustersError
					}
					return tc.workloadClusters[clusterName], nil
				},
				patchNamespace: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
					if tc.patchNamespaceError != nil {
						return nil, tc.patchNamespaceError
					}
					gotPatch = string(data)
					return &corev1.Namespace{}, nil
				},
				enqueueAfter: func(clusterName logicalcluster.LogicalCluster, ns *corev1.Namespace, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, err := r.reconcile(context.Background(), tc.namespace)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)
			require.Equal(t, tc.wantPatch, gotPatch)
		})
	}
}

func location(name string) *schedulingv1alpha1.Location {
	return &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "workloadclusters",
			},
		},
	}
}

func withInstances(location *schedulingv1alpha1.Location, labels map[string]string) *schedulingv1alpha1.Location {
	location.Spec.InstanceSelector = &metav1.LabelSelector{
		MatchLabels: labels,
	}
	return location
}

func binding(name string, workspaceName string) *apisv1alpha1.APIBinding {
	return &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: workspaceName,
				},
			},
		},
	}
}

func bound(binding *apisv1alpha1.APIBinding) *apisv1alpha1.APIBinding {
	conditions.MarkTrue(binding, apisv1alpha1.InitialBindingCompleted)
	return binding
}

func validExport(binding *apisv1alpha1.APIBinding) *apisv1alpha1.APIBinding {
	conditions.MarkTrue(binding, apisv1alpha1.APIExportValid)
	return binding
}

func cluster(name string, uid string) *workloadv1alpha1.WorkloadCluster {
	ret := &workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(uid),
		},
		Spec:   workloadv1alpha1.WorkloadClusterSpec{},
		Status: workloadv1alpha1.WorkloadClusterStatus{},
	}

	return ret
}

func withLabels(cluster *workloadv1alpha1.WorkloadCluster, labels map[string]string) *workloadv1alpha1.WorkloadCluster {
	cluster.Labels = labels
	return cluster
}

func withConditions(cluster *workloadv1alpha1.WorkloadCluster, conditions ...conditionsv1alpha1.Condition) *workloadv1alpha1.WorkloadCluster {
	cluster.Status.Conditions = conditions
	return cluster
}

func unschedulable(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	cluster.Spec.Unschedulable = true
	return cluster
}
