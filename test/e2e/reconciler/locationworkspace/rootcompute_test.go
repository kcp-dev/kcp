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

package locationworkspace

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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRootComputeWorkspace(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClients, err := clientset.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	_ = framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithExtraResources("ingresses.networking.k8s.io", "services", "deployments.apps"),
		framework.WithSyncTarget(computeClusterName, syncTargetName),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
				metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Logf("Patch synctarget with new export")
	patchData := `{"spec":{"supportedAPIExports":[{"workspace":{"path":"root:compute","exportName":"kubernetes"}}]}}`
	_, err = kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 3 {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "services" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}
		if syncTarget.Status.SyncedResources[1].Resource != "ingresses" ||
			syncTarget.Status.SyncedResources[1].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the kubernetes export", consumerWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubernetes",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       "root:compute",
					ExportName: "kubernetes",
				},
			},
		},
	}

	_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	require.Eventually(t, func() bool {
		binding, err := kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.GetOptions{})
		require.NoError(t, err)

		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
