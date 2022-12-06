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

package location

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type LocationCheck func(t *testing.T, l *schedulingv1alpha1.Location)

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

func labelString(expected string) func(t *testing.T, l *schedulingv1alpha1.Location) {
	return func(t *testing.T, got *schedulingv1alpha1.Location) {
		t.Helper()
		require.Equal(t, expected, got.Annotations["scheduling.kcp.dev/labels"])
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

func TestLocationStatusReconciler(t *testing.T) {
	usEast1 := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name: "us-east1",
			Labels: map[string]string{
				"continent": "north-america",
				"country":   "usa",
			},
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "root:org:negotiation-workspace",
				"scheduling.kcp.dev/labels":  "continent=north-america country=usa",
			},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			Description: "Big region at the east coast of the US",
			AvailableSelectorLabels: []schedulingv1alpha1.AvailableSelectorLabel{
				{Key: "gpu", Values: []schedulingv1alpha1.LabelValue{"true"}},
				{Key: "cloud", Values: []schedulingv1alpha1.LabelValue{"aws", "gcp", "azure", "ibm"}},
			},
			InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"region": "us-east1"}},
		},
	}
	usEast1WithoutLabelString := usEast1.DeepCopy()
	usEast1WithoutLabelString.Annotations = nil

	tests := map[string]struct {
		location    *schedulingv1alpha1.Location
		syncTargets map[logicalcluster.Name][]*workloadv1alpha1.SyncTarget

		listSyncTargetError error
		updateLocationError error

		wantLocation        LocationCheck
		wantUpdates         map[string]LocationCheck
		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no SyncTargets": {
			location:            usEast1,
			wantLocation:        and(availableInstances(0), instances(0), labelString("continent=north-america country=usa")),
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no SyncTargets, different label string": {
			location:     usEast1WithoutLabelString,
			wantLocation: and(availableInstances(0), instances(0), labelString("continent=north-america country=usa")),
			wantUpdates: map[string]LocationCheck{
				"us-east1": labelString("continent=north-america country=usa"),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"with sync targets, across two regions": {
			location: usEast1,
			syncTargets: map[logicalcluster.Name][]*workloadv1alpha1.SyncTarget{
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
			wantLocation:        and(availableInstances(1), instances(4)),
			wantReconcileStatus: reconcileStatusContinue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			updates := map[string]*schedulingv1alpha1.Location{}
			r := &statusReconciler{
				listSyncTargets: func(clusterName tenancy.Cluster) ([]*workloadv1alpha1.SyncTarget, error) {
					if tc.listSyncTargetError != nil {
						return nil, tc.listSyncTargetError
					}
					return tc.syncTargets[clusterName.Path()], nil
				},
				updateLocation: func(ctx context.Context, clusterName logicalcluster.Name, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
					if tc.updateLocationError != nil {
						return nil, tc.updateLocationError
					}
					updates[location.Name] = location.DeepCopy()
					return location, nil
				},
				enqueueAfter: func(domain *schedulingv1alpha1.Location, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			location := tc.location.DeepCopy()
			status, err := r.reconcile(context.Background(), location)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)
			tc.wantLocation(t, location)

			// check updates
			for _, l := range updates {
				require.Contains(t, tc.wantUpdates, l.Name, "got unexpected update:\n%s", toYaml(l))
				tc.wantUpdates[l.Name](t, l)
			}
			for name := range tc.wantUpdates {
				require.Contains(t, updates, name, "missing update for %s", name)
			}
		})
	}
}

func cluster(name string) *workloadv1alpha1.SyncTarget {
	ret := &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec:   workloadv1alpha1.SyncTargetSpec{},
		Status: workloadv1alpha1.SyncTargetStatus{},
	}

	return ret
}

func withLabels(cluster *workloadv1alpha1.SyncTarget, labels map[string]string) *workloadv1alpha1.SyncTarget {
	cluster.Labels = labels
	return cluster
}

func withConditions(cluster *workloadv1alpha1.SyncTarget, conditions ...conditionsv1alpha1.Condition) *workloadv1alpha1.SyncTarget {
	cluster.Status.Conditions = conditions
	return cluster
}

func unschedulable(cluster *workloadv1alpha1.SyncTarget) *workloadv1alpha1.SyncTarget {
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
