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
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

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
	orgClusterName := framework.NewOrganizationFixture(t, source)
	locationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())

	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	firstSyncTargetName := fmt.Sprintf("firstsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with no supported APIExports and syncer in %s", locationClusterName)
	_ = framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(firstSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
		framework.WithAPIExports(""),
	).Start(t)

	secondSyncTargetName := fmt.Sprintf("secondsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with global kubernetes APIExports and syncer in %s", locationClusterName)
	_ = framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(secondSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
	).Start(t)

	placementName := "placement-test-supportedapi"
	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, userClusterName.Path(), source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationClusterName.Path()),
		framework.WithPlacementNameBindOption(placementName),
		framework.WithAPIExportsWorkloadBindOption("root:compute:kubernetes"),
	).Bind(t)

	t.Logf("First sync target hash: %s", workloadv1alpha1.ToSyncTargetKey(locationClusterName, firstSyncTargetName))
	scheduledSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(locationClusterName, secondSyncTargetName)

	t.Logf("check placement should be scheduled to synctarget with supported API")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		require.NoError(t, err)

		if value := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]; value != scheduledSyncTargetKey {
			return false, fmt.Sprintf(
				"Internal synctarget annotation for placement should be %s since it is the only SyncTarget with compatible API, but got %q",
				scheduledSyncTargetKey, value)
		}

		condition := conditions.Get(placement, schedulingv1alpha1.PlacementScheduled)
		if condition == nil {
			return false, fmt.Sprintf("no %s condition exists", schedulingv1alpha1.PlacementScheduled)
		}
		if condition.Status == corev1.ConditionTrue {
			return true, ""
		}
		return false, fmt.Sprintf("not done waiting for the placement to be ready, reason: %v - message: %v", condition.Reason, condition.Message)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
