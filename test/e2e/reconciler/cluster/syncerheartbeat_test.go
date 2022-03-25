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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/workload/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestSyncerHeartbeat(t *testing.T) {
	t.Parallel()

	source := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, source)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	t.Log("Creating a fake workload server")
	sink := framework.NewFakeWorkloadServer(t, source, orgClusterName)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.DefaultConfig(t)
	sourceKcpClusterClient, err := kcpclient.NewClusterForConfig(sourceConfig)
	require.NoError(t, err)
	kcpClient := sourceKcpClusterClient.Cluster(wsClusterName)

	t.Log("Creating workload cluster...")
	workloadCluster, err := framework.CreateWorkloadCluster(t, source.Artifact, kcpClient, sink)
	require.NoError(t, err)

	waitForClusterReadyReason(t, ctx, kcpClient.WorkloadV1alpha1().WorkloadClusters(), workloadCluster.Name, workloadv1alpha1.ErrorHeartbeatMissedReason)

	t.Log("Creating workspace syncer...")
	framework.StartWorkspaceSyncer(t, ctx, sets.NewString(), workloadCluster, source, sink)

	waitForClusterReadyReason(t, ctx, kcpClient.WorkloadV1alpha1().WorkloadClusters(), workloadCluster.Name, "")
}

func waitForClusterReadyReason(
	t *testing.T,
	ctx context.Context,
	client workloadclient.WorkloadClusterInterface,
	clusterName string,
	reason string,
) {
	t.Logf("Waiting for cluster %q condition %q to have reason %q", clusterName, workloadv1alpha1.WorkloadClusterReadyCondition, reason)
	require.Eventually(t, func() bool {
		cluster, err := client.Get(ctx, clusterName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("Error getting cluster %q: %v", clusterName, err)
			return false
		}

		// A reason is only supplied to indicate why a cluster is 'not ready'
		wantReady := len(reason) == 0
		if wantReady {
			return conditions.IsTrue(cluster, workloadv1alpha1.WorkloadClusterReadyCondition)
		} else {
			conditionReason := conditions.GetReason(cluster, workloadv1alpha1.WorkloadClusterReadyCondition)
			return conditions.IsFalse(cluster, workloadv1alpha1.WorkloadClusterReadyCondition) && reason == conditionReason
		}

	}, wait.ForeverTestTimeout, time.Millisecond*100)
	t.Logf("Cluster %q condition %s has reason %q", workloadv1alpha1.WorkloadClusterReadyCondition, clusterName, reason)
}
