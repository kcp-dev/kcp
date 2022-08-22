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
	"embed"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/client/dynamic"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestSyncTargetExport(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	schemaClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClients, err := clientset.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Install today service APIResourceSchema into schema workspace %q", schemaClusterName)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(schemaClusterName).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(schemaClusterName), mapper, nil, "apiresourceschema_service.yaml", testFiles)
	require.NoError(t, err)
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(schemaClusterName), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "services",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"test.services.core", "today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(schemaClusterName).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	_ = framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithExtraResources("services"),
		framework.WithSyncTarget(computeClusterName, syncTargetName),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Logf("Patch synctarget with new export")
	patchData := fmt.Sprintf(`{"spec":{"supportedAPIExports":[{"workspace":{"path":%q,"exportName":"services"}}]}}`, schemaClusterName.String())
	_, err = kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Failed to get synctarget: %v", err)
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 2 {
			return false
		}

		t.Logf("synced resources %v", syncTarget.Status.SyncedResources)

		if syncTarget.Status.SyncedResources[1].Resource != "services" ||
			syncTarget.Status.SyncedResources[1].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "cowboys" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaIncomptibleState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
