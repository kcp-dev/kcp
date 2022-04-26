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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

func TestPlacementReconciler(t *testing.T) {
	tests := map[string]struct {
		domains          map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain
		locations        map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location
		workloadClusters map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster
		clusterWorkspace *tenancyv1alpha1.ClusterWorkspace
		namespace        *corev1.Namespace

		getClusterWorkspaceError  error
		getLocationDomainError    error
		listWorkloadClustersError error
		patchNamespaceError       error

		wantPatch           string
		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no domain assignent, no cluster workspace": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			wantError:           true,
			wantReconcileStatus: reconcileStatusStop,
		},
		"no domain assignent, with cluster workspace": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"namespace assignment exists": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{"rootus-east1.test": "Bound"}`,
					},
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"happy case: workspace is assigned, namespace is not placed": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{},
			},
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withLocations(domain(logicalcluster.New("root:org"), "standard-compute", "Workloads"),
						schedulingv1alpha1.LocationDomainLocationDefinition{
							Name:             "us-east1",
							InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "us-east1"}},
						},
					),
				},
			},
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
			wantPatch:           `{"metadata":{"annotation":{"scheduling.kcp.dev/placement":"{\"us-east1+uid-3\":\"Pending\"}"}}}`,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"domain missing": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{},
			},
			wantError:           true,
			wantReconcileStatus: reconcileStatusStop,
		},
		"patch fails": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{},
			},
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withLocations(domain(logicalcluster.New("root:org"), "standard-compute", "Workloads"),
						schedulingv1alpha1.LocationDomainLocationDefinition{
							Name:             "us-east1",
							InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "us-east1"}},
						},
					),
				},
			},
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
				getClusterWorkspace: func(clusterName logicalcluster.LogicalCluster, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
					if tc.getClusterWorkspaceError != nil {
						return nil, tc.getClusterWorkspaceError
					}
					if tc.clusterWorkspace == nil {
						return nil, apierrors.NewNotFound(schema.GroupResource{Group: "tenancy.kcp.dev", Resource: "clusterworkspaces"}, name)
					}
					return tc.clusterWorkspace, nil
				},
				getLocationDomain: func(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error) {
					if tc.getLocationDomainError != nil {
						return nil, tc.getLocationDomainError
					}
					for _, l := range tc.domains[clusterName] {
						if l.Name == name {
							return l, nil
						}
					}
					return nil, apierrors.NewNotFound(schema.GroupResource{Group: "scheduling.kcp.dev", Resource: "locationdomains"}, name)
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

func domain(clusterName logicalcluster.LogicalCluster, name string, domainType string) *schedulingv1alpha1.LocationDomain {
	return &schedulingv1alpha1.LocationDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			ClusterName: clusterName.String(),
		},
		Spec: schedulingv1alpha1.LocationDomainSpec{
			Type: schedulingv1alpha1.LocationDomainType(domainType),
			Instances: schedulingv1alpha1.InstancesReference{
				Resource: schedulingv1alpha1.GroupVersionResource{
					Group:    "workload.kcp.dev",
					Version:  "v1alpha1",
					Resource: "workloadclusters",
				},
				Workspace: &schedulingv1alpha1.WorkspaceExportReference{
					WorkspaceName: "negotiation-workspace",
				},
			},
		},
	}
}

func withLocations(domain *schedulingv1alpha1.LocationDomain, locations ...schedulingv1alpha1.LocationDomainLocationDefinition) *schedulingv1alpha1.LocationDomain {
	domain.Spec.Locations = append(domain.Spec.Locations, locations...)
	return domain
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
