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
	"fmt"
	"math/rand"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSchedulingOnSupportedAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)
	orgPath, _ := framework.NewOrganizationFixture(t, source, framework.TODO_WithoutMultiShardSupport())
	locationPath, locationWS := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	userPath, userWS := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())

	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	firstSyncTargetName := fmt.Sprintf("firstsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with no supported APIExports and syncer in %s", locationPath)
	_ = framework.NewSyncerFixture(t, source, locationPath,
		framework.WithSyncTargetName(firstSyncTargetName),
		framework.WithSyncedUserWorkspaces(userWS),
		framework.WithAPIExports(""),
	).Start(t)

	secondSyncTargetName := fmt.Sprintf("secondsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with global kubernetes APIExports and syncer in %s", locationPath)
	_ = framework.NewSyncerFixture(t, source, locationPath,
		framework.WithSyncTargetName(secondSyncTargetName),
		framework.WithSyncedUserWorkspaces(userWS),
	).Start(t)

	placementName := "placement-test-supportedapi"
	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, userPath, source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationPath),
		framework.WithPlacementNameBindOption(placementName),
		framework.WithAPIExportsWorkloadBindOption("root:compute:kubernetes"),
	).Bind(t)

	t.Logf("First sync target hash: %s", workloadv1alpha1.ToSyncTargetKey(logicalcluster.Name(locationWS.Spec.Cluster), firstSyncTargetName))
	scheduledSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.Name(locationWS.Spec.Cluster), secondSyncTargetName)

	t.Logf("check placement should be scheduled to synctarget with supported API")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(userPath).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
	}, framework.Is(schedulingv1alpha1.PlacementScheduled))
	placement, err := kcpClusterClient.Cluster(userPath).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
	require.NoError(t, err)

	if value := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]; value != scheduledSyncTargetKey {
		t.Errorf("Internal synctarget annotation for placement should be %s since it is the only SyncTarget with compatible API, but got %q",
			scheduledSyncTargetKey, value)
	}
}
