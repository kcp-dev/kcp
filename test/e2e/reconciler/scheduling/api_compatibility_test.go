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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
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

	kcpClusterClient, err := kcpclient.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	firstSyncTargetName := fmt.Sprintf("firstsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with no support APIExports and syncer in %s", locationClusterName)
	_ = framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithExtraResources("roles.rbac.authorization.k8s.io", "rolebindings.rbac.authorization.k8s.io"),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "endpoints"},
			)
			require.NoError(t, err)
		}),
		framework.WithAPIExports(""),
		framework.WithSyncTarget(locationClusterName, firstSyncTargetName),
	).Start(t)

	secondSyncTargetName := fmt.Sprintf("secondsynctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget with global kubernetes APIExports and syncer in %s", locationClusterName)
	secondSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithExtraResources("roles.rbac.authorization.k8s.io", "rolebindings.rbac.authorization.k8s.io"),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "endpoints"},
			)
			require.NoError(t, err)
		}),
		framework.WithSyncTarget(locationClusterName, secondSyncTargetName),
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
		placement, err := kcpClusterClient.SchedulingV1alpha1().Placements().Get(logicalcluster.WithCluster(ctx, userClusterName), placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		if len(placement.Annotations) == 0 || placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] != scheduledSyncTargetKey {
			return false, fmt.Sprintf("Internal synctarget annotation for placement is not correct: %v", placement.Annotations)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
