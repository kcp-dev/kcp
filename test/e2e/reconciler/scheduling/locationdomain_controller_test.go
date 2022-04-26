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

	t.Log("Create a location")
	_, err = kcpClusterClient.Cluster(negotiationClusterName).SchedulingV1alpha1().Locations().Create(ctx, &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "us-east1",
			Labels: map[string]string{"foo": "42"},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.k8s.io",
				Version:  "v1alpha1",
				Resource: "workloadclusters",
			},
			InstanceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"published": "true", "foo": "42"}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for location to show proper status")
	require.Eventually(t, func() bool {
		l, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().Get(ctx, "us-east1", metav1.GetOptions{})
		require.NoError(t, err)

		bs, _ := yaml.Marshal(l)
		klog.Info(string(bs))

		return l.Status.Instances != nil && *l.Status.Instances != 3 &&
			l.Status.AvailableInstances != nil && *l.Status.AvailableInstances != 2
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Remove a workload cluster and wait for Location object to update")
	err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().Delete(ctx, "unready", metav1.DeleteOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		l, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().Get(ctx, "us-east1", metav1.GetOptions{})
		require.NoError(t, err)
		return l.Status.Instances != nil && *l.Status.Instances != 2 &&
			l.Status.AvailableInstances != nil && *l.Status.AvailableInstances != 2
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Change instance selector and wait for Location object to update")
	_, err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().LocationDomains().Patch(ctx, "us-east1", types.JSONPatchType, []byte(`[{"op":"remove","path":"/spec/instanceSelector/matchLabels/foo"}]`), metav1.PatchOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		l, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().Get(ctx, "bar.standard-compute", metav1.GetOptions{})
		require.NoError(t, err)
		return l.Status.Instances != nil && *l.Status.Instances != 1 &&
			l.Status.AvailableInstances != nil && *l.Status.AvailableInstances != 1
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
