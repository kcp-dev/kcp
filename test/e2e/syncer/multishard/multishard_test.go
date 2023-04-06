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

package multishard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	appsv1apply "k8s.io/client-go/applyconfigurations/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/syncer/multishard/workspace1"
	"github.com/kcp-dev/kcp/test/e2e/syncer/multishard/workspace2"
)

// TestSyncingFromMultipleShards ensures that the syncer can effectively sync from several workspaces hosted on distinct shards
// with distinct virtual workspace URLs.
func TestSyncingFromMultipleShards(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamServer := framework.SharedKcpServer(t)
	upstreamConfig := upstreamServer.BaseConfig(t)

	shardNames := upstreamServer.ShardNames()
	if len(shardNames) < 2 {
		t.Skip("Test requires at least 2 shards")
	}

	orgPath, _ := framework.NewOrganizationFixture(t, upstreamServer, framework.TODO_WithoutMultiShardSupport())
	locationPath, locationWs := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.TODO_WithoutMultiShardSupport())
	locationWsName := logicalcluster.Name(locationWs.Spec.Cluster)

	workloadWorkspace1Path, workloadWorkspace1 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithShard(shardNames[0]))
	workloadWorkspace1Name := logicalcluster.Name(workloadWorkspace1.Spec.Cluster)

	workloadWorkspace2Path, workloadWorkspace2 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithShard(shardNames[1]))
	workloadWorkspace2Name := logicalcluster.Name(workloadWorkspace2.Spec.Cluster)

	upstreamKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	// Creating synctarget and deploying the syncer
	syncer := framework.NewSyncerFixture(t, upstreamServer, locationPath, framework.WithSyncedUserWorkspaces(workloadWorkspace1, workloadWorkspace2)).
		CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)

	t.Log("Binding workspace 1 to the location workspace")
	framework.NewBindCompute(t, workloadWorkspace1Name.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWsName.Path()),
	).Bind(t)

	t.Log("Binding workspace 2 to the location workspace")
	framework.NewBindCompute(t, workloadWorkspace2Name.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWsName.Path()),
	).Bind(t)

	err = framework.CreateResources(ctx, workspace1.FS, upstreamConfig, workloadWorkspace1Path)
	require.NoError(t, err)

	err = framework.CreateResources(ctx, workspace2.FS, upstreamConfig, workloadWorkspace2Path)
	require.NoError(t, err)

	downstreamWS1NS := syncer.DownstreamNamespaceFor(t, workloadWorkspace1Name, "default")
	t.Logf("Downstream namespace in workspace 1 is %s", downstreamWS1NS)

	downstreamWS2NS := syncer.DownstreamNamespaceFor(t, workloadWorkspace2Name, "default")
	t.Logf("Downstream namespace in workspace 2 is %s", downstreamWS2NS)

	downstreamKubeClient, err := kubernetes.NewForConfig(syncer.DownstreamConfig)
	require.NoError(t, err)

	framework.Eventually(t, func() (success bool, reason string) {
		_, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS).Get(ctx, "test1", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test1 not found downstream"
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "deployment test1 not synced downstream")

	framework.Eventually(t, func() (success bool, reason string) {
		_, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS2NS).Get(ctx, "test2", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test2 not found downstream"
		}
		require.NoError(t, err)
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "deployment test1 not synced downstream")

	if len(framework.TestConfig.PClusterKubeconfig()) == 0 {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			_, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS).ApplyStatus(ctx, appsv1apply.Deployment("test1", downstreamWS1NS).WithStatus(&appsv1apply.DeploymentStatusApplyConfiguration{
				Replicas:          ptr(int32(1)),
				UpdatedReplicas:   ptr(int32(1)),
				AvailableReplicas: ptr(int32(1)),
				ReadyReplicas:     ptr(int32(1)),
			}), metav1.ApplyOptions{FieldManager: "e2e-test", Force: true})
			return err
		})
		require.NoError(t, err)

		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			_, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS2NS).ApplyStatus(ctx, appsv1apply.Deployment("test2", downstreamWS2NS).WithStatus(&appsv1apply.DeploymentStatusApplyConfiguration{
				Replicas:          ptr(int32(1)),
				UpdatedReplicas:   ptr(int32(1)),
				AvailableReplicas: ptr(int32(0)),
				ReadyReplicas:     ptr(int32(0)),
			}), metav1.ApplyOptions{FieldManager: "e2e-test", Force: true})
			return err
		})
		require.NoError(t, err)
	}

	framework.Eventually(t, func() (success bool, reason string) {
		test1Downstream, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS).Get(ctx, "test1", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test1 not found downstream"
		}
		require.NoError(t, err)

		test1Upstream, err := upstreamKubeClusterClient.AppsV1().Cluster(workloadWorkspace1Path).Deployments("default").Get(ctx, "test1", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test1 not found upstream"
		}
		require.NoError(t, err)
		diff := cmp.Diff(test1Downstream.Status, test1Upstream.Status)
		return len(diff) == 0, fmt.Sprintf("status different between downstream and upstream: %s", diff)
	}, wait.ForeverTestTimeout, time.Millisecond*500, "status of deployment test1 not synced back upstream")

	framework.Eventually(t, func() (success bool, reason string) {
		test2Downstream, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS2NS).Get(ctx, "test2", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test2 not found downstream"
		}
		require.NoError(t, err)

		test2Upstream, err := upstreamKubeClusterClient.AppsV1().Cluster(workloadWorkspace2Path).Deployments("default").Get(ctx, "test2", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, "deployment test1 not found upstream"
		}
		require.NoError(t, err)
		diff := cmp.Diff(test2Downstream.Status, test2Upstream.Status)
		return len(diff) == 0, fmt.Sprintf("status different between downstream and upstream: %s", diff)
	}, wait.ForeverTestTimeout, time.Millisecond*500, "status of deployment test2 not synced back upstream")
}

func ptr[T interface{}](val T) *T {
	other := val
	return &other
}
