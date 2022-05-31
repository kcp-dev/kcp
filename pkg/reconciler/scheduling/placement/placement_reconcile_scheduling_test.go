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

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestPlacementReconciler(t *testing.T) {
	tests := map[string]struct {
		apibindings      map[logicalcluster.Name][]*apisv1alpha1.APIBinding
		locations        map[logicalcluster.Name][]*schedulingv1alpha1.Location
		workloadClusters map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster
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
		"no placement, no binding, no locations, no workload cluster": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"existing placement, no binding, no locations, no workspaces workload cluster": {
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
		"existing placement, with binding, no locations, no workspaces workload cluster": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{"foo": "bar"}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			wantPatch:           `{"metadata":{"annotations":{"scheduling.kcp.dev/placement":null}}}}`,
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"scheduling disabled": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
					Labels: map[string]string{
						"experimental.workloads.kcp.dev/scheduling-disabled": "true",
					},
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{"foo": "bar"}`,
					},
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no placement, with workload cluster, but no location": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				binding("kubernetes", "root:org:negotiation-workspace"),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{logicalcluster.New("root:org:negotiation-workspace"): {
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
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1-az1"), map[string]string{"region": "us-east1"}),
			}},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
			wantPatch:           `{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":"{}"},"resourceVersion":"","uid":""}}`,
		},
		"no placement, with location, no workload cluster, not bound binding": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				validExport(binding("kubernetes", "root:org:negotiation-workspace")),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
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
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(binding("kubernetes", "root:org:negotiation-workspace")),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
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
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(cluster("us-east1-1", "uid-1"), map[string]string{"region": "us-east1"}),
					withLabels(unready(cluster("us-east1-2", "uid-2")), map[string]string{"region": "us-east1"}),
					withLabels(ready(cluster("us-east1-3", "uid-3")), map[string]string{"region": "us-east1"}),
					withLabels(unschedulable(withConditions(cluster("us-east1-4", "uid-4"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"})), map[string]string{"region": "us-east1"}),

					withLabels(ready(cluster("us-west1-1", "uid-11")), map[string]string{"region": "us-west1"}),
					withLabels(ready(cluster("us-west1-2", "uid-12")), map[string]string{"region": "us-west1"}),
				},
				logicalcluster.New("root:org:somewhere-else"): {
					cluster("us-east1-1", "uid-21"),
					cluster("us-east1-2", "uid-22"),
				},
			},
			wantPatch:           `{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":"{\"root:org:negotiation-workspace+us-east1+us-east1-3\":\"Pending\"}"},"resourceVersion":"","uid":""}}`,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"happy case: no location": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(ready(cluster("us-east1-1", "uid-11")), map[string]string{"region": "us-east1"}),
					withLabels(unschedulable(withConditions(cluster("us-east1-2", "uid-12"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"})), map[string]string{"region": "us-east1"}),
				},
			},
			wantPatch:           `{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":"{\"root:org:negotiation-workspace+default+us-east1-1\":\"Pending\"}"},"resourceVersion":"","uid":""}}`,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"rescheduling": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					ClusterName:     "root:org:ws",
					ResourceVersion: "42",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{
"root:org:negotiation-workspace+us-east1+us-east1-1":"Pending",
"root:org:negotiation-workspace+us-east2+us-east2-1":"Bound",
"root:org:negotiation-workspace+us-east3+us-east3-1":"Bound",
"root:org:negotiation-workspace+us-east4+us-east4-1":"Removing",
"root:org:negotiation-workspace+us-east5+us-east5-1":"Unbound",
"root:org:negotiation-workspace+us-east6+us-east6-1":"Bound",
"root:org:negotiation-workspace+us-east7+us-east7-1":"Bound"
}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
				withInstances(location("us-east2"), map[string]string{"region": "us-east2"}),
				withInstances(location("us-east3"), map[string]string{"region": "us-east3"}),
				withInstances(location("us-east4"), map[string]string{"region": "us-east4"}),
				withInstances(location("us-east5"), map[string]string{"region": "us-east5"}),
				withInstances(location("us-east6"), map[string]string{"region": "us-east6"}),
				withInstances(location("us-east7"), map[string]string{"region": "us-east7"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					unschedulable(evicting(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"}))), // should be rescheduled hard because pending
					ready(withLabels(cluster("us-east1-2", "uid-12"), map[string]string{"region": "us-east1"})),

					unschedulable(evicting(withLabels(cluster("us-east2-1", "uid-21"), map[string]string{"region": "us-east2"}))), // should be rescheduled softly because bound
					ready(withLabels(cluster("us-east2-2", "uid-22"), map[string]string{"region": "us-east2"})),

					ready(withLabels(cluster("us-east3-1", "uid-31"), map[string]string{"region": "us-east3"})), // should be kept because bound and ready
					ready(withLabels(cluster("us-east3-2", "uid-32"), map[string]string{"region": "us-east3"})),

					unschedulable(evicting(withLabels(cluster("us-east4-1", "uid-41"), map[string]string{"region": "us-east4"}))), // should be rescheduled because removing
					ready(withLabels(cluster("us-east4-2", "uid-42"), map[string]string{"region": "us-east4"})),

					unschedulable(withLabels(cluster("us-east5-1", "uid-51"), map[string]string{"region": "us-east5"})), // should be rescheduled hard because unbound
					ready(withLabels(cluster("us-east5-2", "uid-52"), map[string]string{"region": "us-east5"})),

					unschedulable(withLabels(cluster("us-east6-1", "uid-61"), map[string]string{"region": "us-east6"})), // should be kept bound because not evicting
					ready(withLabels(cluster("us-east6-2", "uid-62"), map[string]string{"region": "us-east6"})),

					unschedulable(evictingSoon(withLabels(cluster("us-east7-1", "uid-71"), map[string]string{"region": "us-east7"}))), // should be kept bound because not yet evicting
					ready(withLabels(cluster("us-east7-2", "uid-72"), map[string]string{"region": "us-east7"})),
				},
			},
			wantPatch: fmt.Sprintf(`{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":%q},"resourceVersion":"42","uid":""}}`,
				`{"root:org:negotiation-workspace+us-east2+us-east2-1":"Removing",`+
					`"root:org:negotiation-workspace+us-east2+us-east2-2":"Pending",`+
					`"root:org:negotiation-workspace+us-east3+us-east3-1":"Bound",`+
					`"root:org:negotiation-workspace+us-east4+us-east4-1":"Removing",`+
					`"root:org:negotiation-workspace+us-east4+us-east4-2":"Pending",`+
					`"root:org:negotiation-workspace+us-east6+us-east6-1":"Bound",`+
					`"root:org:negotiation-workspace+us-east7+us-east7-1":"Bound"}`),
			wantReconcileStatus: reconcileStatusContinue,
		},
		"location gone": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					ClusterName:     "root:org:ws",
					ResourceVersion: "42",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{
"root:org:negotiation-workspace+us-east7+us-east7-1":"Pending",
"root:org:negotiation-workspace+us-east7+us-east7-2":"Bound",
"root:org:negotiation-workspace+us-east7+us-east7-3":"Removing",
"root:org:negotiation-workspace+us-east7+us-east7-4":"Unbound",
"root:org:negotiation-workspace+us-east7+us-east7-5":"Bound"
}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
				withInstances(location("us"), map[string]string{"foo": "bar"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					unschedulable(evicting(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"}))), // should be rescheduled hard because pending
					ready(withLabels(cluster("us-east1-2", "uid-12"), map[string]string{"region": "us-east1"})),

					ready(withLabels(cluster("us-east7-1", "uid-71"), map[string]string{"region": "us-east7"})),               // dropped because pending
					ready(withLabels(cluster("us-east7-2", "uid-72"), map[string]string{"region": "us-east7"})),               // set to Removing because bound
					ready(withLabels(cluster("us-east7-3", "uid-73"), map[string]string{"region": "us-east7"})),               // kept at Removing
					ready(withLabels(cluster("us-east7-4", "uid-74"), map[string]string{"region": "us-east7"})),               // dropped because unbound
					ready(withLabels(cluster("us-east7-5", "uid-75"), map[string]string{"region": "us-east7", "foo": "bar"})), // revived because also in another location
				},
			},
			wantPatch: fmt.Sprintf(`{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":%q},"resourceVersion":"42","uid":""}}`,
				`{"root:org:negotiation-workspace+us+us-east7-5":"Bound",`+
					`"root:org:negotiation-workspace+us-east7+us-east7-2":"Removing",`+
					`"root:org:negotiation-workspace+us-east7+us-east7-3":"Removing"}`),
			wantReconcileStatus: reconcileStatusContinue,
		},
		"unschedulable": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					ClusterName:     "root:org:ws",
					ResourceVersion: "42",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					unschedulable(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"})),
				},
			},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"evicting, not scheduled yet": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					ClusterName:     "root:org:ws",
					ResourceVersion: "42",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					evicting(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"})),
				},
			},
			wantRequeue:         time.Minute * 2,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"different negotiation workspace": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
					Annotations: map[string]string{
						"scheduling.kcp.dev/placement": `{
"root:org:negotiation-workspace+us-east1+us-east1-1":"Pending",
"root:org:elsewhere+us-east2+us-east2-1":"Bound",
}`,
					},
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
				},
				logicalcluster.New("root:org:elsewhere"): {
					withInstances(location("us-east2"), map[string]string{"region": "us-east2"}),
				},
			},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					ready(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"})),
					ready(withLabels(cluster("us-east1-1", "uid-11"), map[string]string{"region": "us-east1"})),
				},
				logicalcluster.New("root:org:elsewhere"): {
					ready(withLabels(cluster("us-east2-1", "uid-21"), map[string]string{"region": "us-east2"})),
					ready(withLabels(cluster("us-east2-1", "uid-21"), map[string]string{"region": "us-east2"})),
				},
			},
			wantPatch: fmt.Sprintf(`{"metadata":{"annotations":{"internal.scheduling.kcp.dev/negotiation-workspace":"root:org:negotiation-workspace","scheduling.kcp.dev/placement":%q},"resourceVersion":"","uid":""}}`,
				`{"root:org:negotiation-workspace+us-east1+us-east1-1":"Pending"}`),
			wantReconcileStatus: reconcileStatusContinue,
		},
		"patch fails": {
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					ClusterName: "root:org:ws",
				},
			},
			apibindings: map[logicalcluster.Name][]*apisv1alpha1.APIBinding{logicalcluster.New("root:org:ws"): {
				bound(validExport(binding("kubernetes", "root:org:negotiation-workspace"))),
			}},
			locations: map[logicalcluster.Name][]*schedulingv1alpha1.Location{logicalcluster.New("root:org:negotiation-workspace"): {
				withInstances(location("us-east1"), map[string]string{"region": "us-east1"}),
			}},
			workloadClusters: map[logicalcluster.Name][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(ready(cluster("us-east1-3", "uid-3")), map[string]string{"region": "us-east1"}),
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
				listLocations: func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error) {
					if tc.listLocationsError != nil {
						return nil, tc.listLocationsError
					}
					return tc.locations[clusterName], nil
				},
				listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
					if tc.listLocationsError != nil {
						return nil, tc.listLocationsError
					}
					return tc.apibindings[clusterName], nil
				},
				listWorkloadClusters: func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error) {
					if tc.listWorkloadClustersError != nil {
						return nil, tc.listWorkloadClustersError
					}
					return tc.workloadClusters[clusterName], nil
				},
				patchNamespace: func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
					if tc.patchNamespaceError != nil {
						return nil, tc.patchNamespaceError
					}
					gotPatch = string(data)
					return &corev1.Namespace{}, nil
				},
				enqueueAfter: func(clusterName logicalcluster.Name, ns *corev1.Namespace, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, _, err := r.reconcile(context.Background(), tc.namespace)
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

func binding(name string, Path string) *apisv1alpha1.APIBinding {
	return &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       Path,
					ExportName: "kubernetes",
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

func ready(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	return withConditions(cluster, conditionsv1alpha1.Condition{
		Type:   "Ready",
		Status: corev1.ConditionTrue,
	})
}

func unready(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	return withConditions(cluster, conditionsv1alpha1.Condition{
		Type:    "Ready",
		Reason:  "NotReady",
		Message: "Cluster is not ready",
		Status:  corev1.ConditionFalse,
	})
}

func evicting(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	minuteAgo := metav1.NewTime(time.Now().Add(-time.Minute))
	cluster.Spec.EvictAfter = &minuteAgo
	return cluster
}

func evictingSoon(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	inSomeMin := metav1.NewTime(time.Now().Add(5 * time.Minute))
	cluster.Spec.EvictAfter = &inSomeMin
	return cluster
}

func unschedulable(cluster *workloadv1alpha1.WorkloadCluster) *workloadv1alpha1.WorkloadCluster {
	cluster.Spec.Unschedulable = true
	return cluster
}
