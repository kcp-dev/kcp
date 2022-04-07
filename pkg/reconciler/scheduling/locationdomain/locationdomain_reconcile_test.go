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

package locationdomain

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

type LocationCheck func(t *testing.T, l *schedulingv1alpha1.Location)

func someLocationWeDontCareAboutInDetail(t *testing.T, got *schedulingv1alpha1.Location) {}

func equals(expected *schedulingv1alpha1.Location) func(t *testing.T, l *schedulingv1alpha1.Location) {
	return func(t *testing.T, got *schedulingv1alpha1.Location) {
		t.Helper()
		require.Equal(t, expected, got)
	}
}

func availableInstances(expected uint32) func(t *testing.T, l *schedulingv1alpha1.Location) {
	return func(t *testing.T, got *schedulingv1alpha1.Location) {
		t.Helper()
		require.NotNilf(t, got.Status.AvailableInstances, "expected %d available instances, not nil", expected)
		require.Equal(t, expected, *got.Status.AvailableInstances)
	}
}

func instances(expected uint32) func(t *testing.T, l *schedulingv1alpha1.Location) {
	return func(t *testing.T, got *schedulingv1alpha1.Location) {
		t.Helper()
		require.NotNilf(t, got.Status.Instances, "expected %d instances, not nil", expected)
		require.Equal(t, expected, *got.Status.Instances)
	}
}

func and(fns ...LocationCheck) LocationCheck {
	return func(t *testing.T, l *schedulingv1alpha1.Location) {
		t.Helper()
		for _, fn := range fns {
			fn(t, l)
		}
	}
}

func TestLocationReconciler(t *testing.T) {
	usEast1 := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name: "us-east1.test",
			Labels: map[string]string{
				"continent": "north-america",
				"country":   "usa",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: schedulingv1alpha1.SchemeGroupVersion.String(),
					Kind:       "LocationDomain",
					Name:       "test",
				},
			},
			Annotations: map[string]string{
				"scheduling.kcp.dev/labels": "continent=north-america country=usa",
			},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
				Description: "Big region at the east coast of the US",
				AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
					{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
					{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
				},
			},
			Type: "Workloads",
			Domain: schedulingv1alpha1.LocationDomainReference{
				Name:      "test",
				Workspace: "root:org",
			},
		},
	}
	usEast1WithSlightlyChangedSpec := usEast1.DeepCopy()
	usEast1WithSlightlyChangedSpec.Spec.LocationSpecBase.Description = "TODO"

	tests := map[string]struct {
		domain           *schedulingv1alpha1.LocationDomain
		locations        map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location
		workloadClusters map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster

		listLocationError         error
		listWorkloadClusterError  error
		deleteLocationError       error
		updateLocationError       error
		updateLocationStatusError error
		createLocationError       error

		wantCreates       map[string]LocationCheck
		wantUpdates       map[string]LocationCheck
		wantStatusUpdates map[string]LocationCheck
		wantDeletes       map[string]struct{}

		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no WorkloadClusters, no locations": {
			domain:              domain(logicalcluster.New("root:org"), "test", "Workloads"),
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no WorkloadClusters": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the east coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-east1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
			),
			wantCreates: map[string]LocationCheck{
				"us-east1.test": equals(usEast1),
			},
			wantStatusUpdates: map[string]LocationCheck{
				"us-east1.test": and(availableInstances(0), instances(0)),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no WorkloadClusters, pre-existing location with same spec": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the east coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-east1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
			),
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{
				logicalcluster.New("root:org"): {usEast1},
			},
			wantStatusUpdates: map[string]LocationCheck{
				"us-east1.test": and(availableInstances(0), instances(0)),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no WorkloadClusters, pre-existing location with different spec": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the east coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-east1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
			),
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{
				logicalcluster.New("root:org"): {usEast1WithSlightlyChangedSpec},
			},
			wantUpdates: map[string]LocationCheck{
				"us-east1.test": equals(usEast1),
			},
			wantStatusUpdates: map[string]LocationCheck{
				"us-east1.test": and(availableInstances(0), instances(0)),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"removed location from domain": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the west coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-west1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
			),
			locations: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{
				logicalcluster.New("root:org"): {usEast1},
			},
			wantCreates: map[string]LocationCheck{
				"us-west1.test": someLocationWeDontCareAboutInDetail,
			},
			wantStatusUpdates: map[string]LocationCheck{
				"us-west1.test": someLocationWeDontCareAboutInDetail,
			},
			wantDeletes: map[string]struct{}{
				"us-east1.test": {},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"added a location": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the east coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-east1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the west coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:   "us-west1",
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
				},
			),
			locations:           map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.Location{logicalcluster.New("root:org"): {usEast1}},
			wantCreates:         map[string]LocationCheck{"us-west1.test": someLocationWeDontCareAboutInDetail},
			wantStatusUpdates:   map[string]LocationCheck{"us-east1.test": someLocationWeDontCareAboutInDetail, "us-west1.test": someLocationWeDontCareAboutInDetail},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"with workload clusters, across two regions": {
			domain: withLocations(domain(logicalcluster.New("root:org"), "test", "Workloads"),
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the east coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:             "us-east1",
					Labels:           map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
					InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "us-east1"}},
				},
				schedulingv1alpha1.LocationDomainLocationDefinition{
					LocationSpecBase: schedulingv1alpha1.LocationSpecBase{
						Description: "Big region at the west coast of the US",
						AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
							{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
							{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
						},
					},
					Name:             "us-west1",
					Labels:           map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{"continent": "north-america", "country": "usa"},
					InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "us-west1"}},
				},
			),
			workloadClusters: map[logicalcluster.LogicalCluster][]*workloadv1alpha1.WorkloadCluster{
				logicalcluster.New("root:org:negotiation-workspace"): {
					withLabels(cluster("us-east1-1"), map[string]string{"region": "us-east1"}),
					withLabels(withConditions(cluster("us-east1-2"), conditionsv1alpha1.Condition{Type: "Ready", Status: "False"}), map[string]string{"region": "us-east1"}),
					withLabels(withConditions(cluster("us-east1-3"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-east1"}),
					withLabels(unschedulable(withConditions(cluster("us-east1-4"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"})), map[string]string{"region": "us-east1"}),

					withLabels(withConditions(cluster("us-west1-1"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-west1"}),
					withLabels(withConditions(cluster("us-west1-2"), conditionsv1alpha1.Condition{Type: "Ready", Status: "True"}), map[string]string{"region": "us-west1"}),
				},
				logicalcluster.New("root:org:somewhere-else"): {
					cluster("us-east1-1"),
					cluster("us-east1-2"),
				},
			},
			wantCreates: map[string]LocationCheck{
				"us-east1.test": someLocationWeDontCareAboutInDetail,
				"us-west1.test": someLocationWeDontCareAboutInDetail,
			},
			wantStatusUpdates: map[string]LocationCheck{
				"us-east1.test": and(availableInstances(1), instances(4)),
				"us-west1.test": and(availableInstances(2), instances(2)),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			creates := map[string]*schedulingv1alpha1.Location{}
			updates := map[string]*schedulingv1alpha1.Location{}
			statusUpdates := map[string]*schedulingv1alpha1.Location{}
			deletes := map[string]struct{}{}
			r := &locationReconciler{
				listWorkloadClusters: func(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error) {
					if tc.listWorkloadClusterError != nil {
						return nil, tc.listWorkloadClusterError
					}
					return tc.workloadClusters[clusterName], nil
				},
				listLocations: func(clusterName logicalcluster.LogicalCluster, domainName string) ([]*schedulingv1alpha1.Location, error) {
					if tc.listLocationError != nil {
						return nil, tc.listLocationError
					}
					return tc.locations[clusterName], nil
				},
				createLocation: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
					if tc.createLocationError != nil {
						return nil, tc.createLocationError
					}
					creates[location.Name] = location.DeepCopy()
					return location, nil
				},
				updateLocation: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
					if tc.updateLocationError != nil {
						return nil, tc.updateLocationError
					}
					updates[location.Name] = location.DeepCopy()
					return location, nil
				},
				updateLocationStatus: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
					if tc.updateLocationError != nil {
						return nil, tc.updateLocationError
					}
					statusUpdates[location.Name] = location.DeepCopy()
					return location, nil
				},
				deleteLocation: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string) error {
					if tc.deleteLocationError != nil {
						return tc.deleteLocationError
					}
					deletes[name] = struct{}{}
					return nil
				},
				enqueueAfter: func(clusterName logicalcluster.LogicalCluster, domain *schedulingv1alpha1.LocationDomain, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, err := r.reconcile(context.Background(), tc.domain)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)

			// check creates
			for _, l := range creates {
				require.Contains(t, tc.wantCreates, l.Name, "got unexpected create:\n%s", toYaml(l))
				tc.wantCreates[l.Name](t, l)
			}
			for name := range tc.wantCreates {
				require.Contains(t, creates, name, "missing create of %s", name)
			}

			// check updates
			for _, l := range updates {
				require.Contains(t, tc.wantUpdates, l.Name, "got unexpected update:\n%s", toYaml(l))
				tc.wantUpdates[l.Name](t, l)
			}
			for name := range tc.wantUpdates {
				require.Contains(t, updates, name, "missing update for %s", name)
			}

			// check status updates
			for _, l := range statusUpdates {
				require.Contains(t, tc.wantStatusUpdates, l.Name, "got unexpected status update:\n%s", toYaml(l))
				tc.wantStatusUpdates[l.Name](t, l)
			}
			for name := range tc.wantStatusUpdates {
				require.Contains(t, statusUpdates, name, "missing status update for %s", name)
			}

			// check deletes
			for name := range deletes {
				require.Contains(t, tc.wantDeletes, name, "got unexpected delete of %q", name)
			}
			for name := range tc.wantDeletes {
				require.Contains(t, deletes, name, "missing delete of %q", name)
			}
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

func cluster(name string) *workloadv1alpha1.WorkloadCluster {
	ret := &workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
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

func toYaml(obj interface{}) string {
	bytes, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}
