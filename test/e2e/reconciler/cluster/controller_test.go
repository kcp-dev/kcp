/*
Copyright 2021 The KCP Authors.

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

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclient "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog/v2"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/workload"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	wildwestclient "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/typed/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const testNamespace = "cluster-controller-test"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestClusterController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		client     wildwestclient.WildwestV1alpha1Interface
		coreClient corev1client.CoreV1Interface
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, servers map[string]runningServer)
	}{
		{
			name: "create an object, expect spec and status to sync to sink, then delete",
			work: func(ctx context.Context, t *testing.T, servers map[string]runningServer) {
				t.Logf("Creating cowboy timothy")
				cowboy, err := servers[sourceClusterName].client.Cowboys(testNamespace).Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "timothy",
						Labels: map[string]string{
							nscontroller.ClusterLabel: sinkClusterName,
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create cowboy")

				nsLocator := syncer.NamespaceLocator{LogicalCluster: logicalcluster.From(cowboy), Namespace: cowboy.Namespace}
				targetNamespace, err := syncer.PhysicalClusterNamespaceName(nsLocator)
				t.Logf("Expecting namespace %s to show up in sink", targetNamespace)
				require.NoError(t, err, "Error determining namespace mapping for %v", nsLocator)
				require.Eventually(t, func() bool {
					if _, err = servers[sinkClusterName].coreClient.Namespaces().Get(ctx, targetNamespace, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false
						}
						require.NoError(t, err, "Error getting namespace %q", targetNamespace)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "expected namespace to be created in sink")

				t.Logf("Expecting same spec to show up in sink")
				require.Eventually(t, func() bool {
					if got, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false
						}
						klog.Errorf("Error getting cowboy %q in sink: %v", cowboy.Name, err)
						return false
					} else if diff := cmp.Diff(cowboy.Spec, got.Spec); diff != "" {
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "expected cowboy to be synced to sink with right spec")

				t.Logf("Patching status in sink")
				updated, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Patch(ctx, cowboy.Name, types.MergePatchType, []byte(`{"status":{"result":"giddyup"}}`), metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch cowboy status in sink")

				t.Logf("Expecting status update to show up in source")
				require.Eventually(t, func() bool {
					if got, err := servers[sourceClusterName].client.Cowboys(testNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false
						}
						klog.Errorf("Error getting cowboy %q in source: %v", cowboy.Name, err)
						return false
					} else if diff := cmp.Diff(updated.Status, got.Status); diff != "" {
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "expected source to show status change")

				err = servers[sourceClusterName].client.Cowboys(testNamespace).Delete(ctx, cowboy.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "error deleting source cowboy")

				// TODO(ncdc): the expect code for cowboys currently expects the cowboy to exist. See if we can adjust it
				// so we can reuse that here instead of polling.
				require.Eventually(t, func() bool {
					_, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected sink cowboy to be deleted")
			},
		},
	}

	source := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, source)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			t.Log("Creating a workspace")
			wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

			// clients
			sourceConfig := source.DefaultConfig(t)
			sourceKubeClusterClient, err := kubernetesclient.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceCrdClusterClient, err := apiextensionsclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceWildwestClusterClient, err := wildwestclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)

			sourceCrdClient := sourceCrdClusterClient.Cluster(wsClusterName)
			sourceKubeClient := sourceKubeClusterClient.Cluster(wsClusterName)
			sourceWildwestClient := sourceWildwestClusterClient.Cluster(wsClusterName)

			t.Log("Installing test CRDs into source cluster...")
			err = configcrds.Create(ctx, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: workload.GroupName, Resource: "workloadclusters"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "apiresourceimports"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
			)
			require.NoError(t, err)
			fixturewildwest.Create(t, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})

			syncerFixture := framework.NewSyncerFixture(t, sets.NewString("cowboys.wildwest.dev"), source, orgClusterName, wsClusterName)
			sink := syncerFixture.RunningServer

			sinkConfig := sink.DefaultConfig(t)
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(sinkConfig)
			require.NoError(t, err)
			sinkWildwestClient, err := wildwestclientset.NewForConfig(sinkConfig)
			require.NoError(t, err)

			t.Log("Installing test CRDs into sink cluster...")
			fixturewildwest.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})

			t.Log("Starting syncer...")
			syncerFixture.Start(t, ctx)

			t.Log("Creating namespace in source cluster...")
			_, err = sourceKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			runningServers := map[string]runningServer{
				sourceClusterName: {
					RunningServer: source,
					client:        sourceWildwestClient.WildwestV1alpha1(),
					coreClient:    sourceKubeClient.CoreV1(),
				},
				sinkClusterName: {
					RunningServer: sink,
					client:        sinkWildwestClient.WildwestV1alpha1(),
					coreClient:    syncerFixture.KubeClient.CoreV1(),
				},
			}

			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		})
	}
}
