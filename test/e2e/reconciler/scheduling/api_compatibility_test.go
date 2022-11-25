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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

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
	locationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	firstSyncTargetName := fmt.Sprintf("firstsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with no support APIExports and syncer in %s", locationClusterName)
	_ = framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(firstSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
		framework.WithAPIExports(""),
	).Start(t)

	secondSyncTargetName := fmt.Sprintf("secondsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with global kubernetes APIExports and syncer in %s", locationClusterName)
	secondSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(secondSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
	).Start(t)

	placementName := "placement-test-supportedapi"
	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, userClusterName, source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationClusterName),
		framework.WithPlacementNameBindOption(placementName),
	).Bind(t)

	scheduledSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(secondSyncerFixture.SyncerConfig.SyncTargetWorkspace, secondSyncTargetName)
	t.Logf("check placement should be scheduled to synctarget with supported API")
	framework.Eventually(t, func() (bool, string) {
		syncTargets, err := kcpClusterClient.Cluster(locationClusterName).WorkloadV1alpha1().SyncTargets().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get synctargets: %v", err)
		}

		for _, s := range syncTargets.Items {
			t.Logf("synctarget apiexports %s: %v", s.Name, s.Spec.SupportedAPIExports[0].Workspace)
			t.Logf("synctarget %s: %v", s.Name, s.Status.SyncedResources)
		}

		apiBindings, err := kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get apibindings: %v", err)
		}
		for _, s := range apiBindings.Items {
			if _, ok := s.Annotations[workloadv1alpha1.AnnotationAPIExportWorkload]; ok {
				t.Logf("apibinding %s: %v", s.Name, s.Status.BoundResources)
			}
		}

		placement, err := kcpClusterClient.Cluster(userClusterName).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		if len(placement.Annotations) == 0 || placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] != scheduledSyncTargetKey {
			return false, fmt.Sprintf("Internal synctarget annotation for placement is not correct: %v", placement.Annotations)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
