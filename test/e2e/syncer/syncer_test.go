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

package syncer

import (
	"context"
	"embed"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubernetesclientset "k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/syncer"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestSyncerLifecycle(t *testing.T) {
	t.Parallel()

	upstreamServer := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, upstreamServer)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, upstreamServer, orgClusterName, "Universal")

	resourcesToSync := sets.NewString("deployments.apps")
	syncerFixture := framework.NewSyncerFixture(t, resourcesToSync, upstreamServer, orgClusterName, wsClusterName)

	downstreamServer := syncerFixture.RunningServer
	downstreamConfig := downstreamServer.DefaultConfig(t)
	downstreamCrdClient, err := apiextensionsclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	t.Log("Install deployment crd in the downstream server to trigger the api importer")
	kubefixtures.Create(t, downstreamCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
		metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
	)

	syncerFixture.Start(t, ctx)

	upstreamConfig := upstreamServer.DefaultConfig(t)
	upstreamKubeClusterClient, err := kubernetesclientset.NewClusterForConfig(upstreamConfig)
	require.NoError(t, err)
	upstreamKubeClient := upstreamKubeClusterClient.Cluster(wsClusterName)

	t.Log("Creating upstream namespace...")
	upstreamNamespace, err := upstreamKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-syncer",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Creating upstream deployment...")

	deploymentYAML, err := embeddedResources.ReadFile("deployment.yaml")
	require.NoError(t, err, "failed to read embedded deployment")

	var deployment *appsv1.Deployment
	err = yaml.Unmarshal(deploymentYAML, &deployment)
	require.NoError(t, err, "failed to unmarshal deployment")

	// This test created a new workspace that initially lacked support for deployments, but once the
	// workload cluster went ready (checked by the syncer fixture's Start method) the api importer
	// will have enabled deployments in the logical cluster.
	upstreamDeployment, err := upstreamKubeClient.AppsV1().Deployments(upstreamNamespace.Name).Create(ctx, deployment, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create deployment")

	t.Log("Checking that deployment was synced...")

	downstreamKubeClient, err := kubernetesclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	nsLocator := syncer.NamespaceLocator{LogicalCluster: logicalcluster.From(upstreamNamespace), Namespace: upstreamNamespace.Name}
	downstreamNamespaceName, err := syncer.PhysicalClusterNamespaceName(nsLocator)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		deployment, err = downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("saw an error waiting for deployment to be synced: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "deployment was not synced")

	// TODO(marun) Validate that a deployment pod can interact with
	// the kcp api once the syncer can be tested with kind
}
