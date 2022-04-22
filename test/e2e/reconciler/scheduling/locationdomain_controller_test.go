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

package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestLocationDomainController(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, source)
	negotiationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	t.Log("Creating some fake workload servers")
	sinksByLabel := map[string][]framework.RunningServer{
		"foo": {
			framework.NewFakeWorkloadServer(t, source, orgClusterName),
			framework.NewFakeWorkloadServer(t, source, orgClusterName),
		},
		"bar": {
			framework.NewFakeWorkloadServer(t, source, orgClusterName),
		},
	}

	kcpClusterClient, err := kcpclient.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)

	for label, sinks := range sinksByLabel {
		for _, sink := range sinks {
			t.Logf("Creating workload cluster for published=true %s=42", label)
			workloadCluster, err := framework.CreateWorkloadCluster(t, source.Artifact, kcpClusterClient.Cluster(negotiationClusterName), sink, func(cluster *workloadv1alpha1.WorkloadCluster) {
				if cluster.Labels == nil {
					cluster.Labels = make(map[string]string)
				}
				cluster.Labels[label] = "42"
				cluster.Labels["published"] = "true"
			})
			require.NoError(t, err)

			t.Logf("Creating fake workload cluster %q heartbeater for published=true %s=42", workloadCluster.Name, label)
			// nolint: errcheck
			go wait.UntilWithContext(ctx, func(ctx context.Context) {
				ws, err := kcpClusterClient.Cluster(negotiationClusterName).WorkloadV1alpha1().WorkloadClusters().Get(ctx, workloadCluster.Name, metav1.GetOptions{})
				if err != nil {
					klog.Errorf("Failed to get workload cluster %s|%s: %v", negotiationClusterName, workloadCluster.Name, err)
					return
				}
				now := metav1.Now()
				ws.Status.LastSyncerHeartbeatTime = &now
				ws, err = kcpClusterClient.Cluster(negotiationClusterName).WorkloadV1alpha1().WorkloadClusters().UpdateStatus(ctx, ws, metav1.UpdateOptions{})
				if err != nil {
					klog.Errorf("Failed to update workload cluster %s|%s: %v", negotiationClusterName, workloadCluster.Name, err)
					return
				}

				klog.Infof("Heartbeated workload cluster %s|%s: %v", negotiationClusterName, workloadCluster.Name, ws.Status.LastSyncerHeartbeatTime)
			}, time.Second)
		}
	}

	t.Log("Creating workload cluster for published=false foo=42")
	unpublished := framework.NewFakeWorkloadServer(t, source, orgClusterName)
	_, err = framework.CreateWorkloadCluster(t, source.Artifact, kcpClusterClient.Cluster(negotiationClusterName), unpublished, func(cluster *workloadv1alpha1.WorkloadCluster) {
		if cluster.Labels == nil {
			cluster.Labels = make(map[string]string)
		}
		cluster.Labels["foo"] = "42"
		cluster.Labels["published"] = "false"
	})
	require.NoError(t, err)

	t.Log("Creating unready workload cluster for published=true foo=42")
	_, err = kcpClusterClient.Cluster(negotiationClusterName).WorkloadV1alpha1().WorkloadClusters().Create(ctx, &workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unready",
			Labels: map[string]string{
				"foo":       "42",
				"published": "true",
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create a location domain")
	_, err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().LocationDomains().Create(ctx, &schedulingv1alpha1.LocationDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: "standard-compute",
		},
		Spec: schedulingv1alpha1.LocationDomainSpec{
			Type: "Workloads",
			Instances: schedulingv1alpha1.InstancesReference{
				Resource: schedulingv1alpha1.GroupVersionResource{
					Group:    "workload.kcp.dev",
					Version:  "v1alpha1",
					Resource: "workloadclusters",
				},
				Workspace: &schedulingv1alpha1.WorkspaceExportReference{
					WorkspaceName: schedulingv1alpha1.WorkspaceName(negotiationClusterName.Base()),
				},
			},
			Locations: []schedulingv1alpha1.LocationDomainLocationDefinition{
				{
					Name:             "foo",
					InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"published": "true", "foo": "42"}},
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{
						"foo": "42",
					},
				},
				{
					Name:             "bar",
					InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"published": "true", "bar": "42"}},
					Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{
						"bar": "42",
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for locations to show up")
	require.Eventually(t, func() bool {
		locations, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		bs, _ := yaml.Marshal(locations)
		klog.Info(string(bs))

		if len(locations.Items) != 2 {
			t.Logf("Expected %d locations, got %d", 2, len(locations.Items))
			return false
		}

		for _, l := range locations.Items {
			switch l.Name {
			case "foo.standard-compute":
				if l.Status.Instances == nil || *l.Status.Instances != 3 {
					return false
				}
				if l.Status.AvailableInstances == nil || *l.Status.AvailableInstances != 2 {
					return false
				}
			case "bar.standard-compute":
				if l.Status.Instances == nil || *l.Status.Instances != 1 {
					return false
				}
			default:
				klog.Errorf("Unexpected location %s", l.Name)
				return false
			}
		}

		return true
	}, wait.ForeverTestTimeout, time.Second)

	t.Log("Remove the location definition and wait for Location object to be deleted")
	_, err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().LocationDomains().Patch(ctx, "standard-compute", types.JSONPatchType, []byte(`[{"op":"remove","path":"/spec/locations/1"}]`), metav1.PatchOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().Get(ctx, "bar.standard-compute", metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, time.Minute, time.Second)
}
