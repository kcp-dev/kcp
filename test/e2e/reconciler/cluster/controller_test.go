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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	wildwestclusterclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	wildwestv1alpha1client "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/typed/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const testNamespace = "cluster-controller-test"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestClusterController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	type runningServer struct {
		client     wildwestv1alpha1client.WildwestV1alpha1Interface
		coreClient corev1client.CoreV1Interface
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, servers map[string]runningServer, syncerFixture *framework.StartedSyncerFixture)
	}{
		{
			name: "create an object, expect spec and status to sync to sink, then delete",
			work: func(ctx context.Context, t *testing.T, servers map[string]runningServer, syncerFixture *framework.StartedSyncerFixture) {
				t.Helper()
				kcpClient, err := kcpclientset.NewForConfig(syncerFixture.SyncerConfig.UpstreamConfig)
				require.NoError(t, err)

				syncTarget, err := kcpClient.Cluster(syncerFixture.SyncerConfig.SyncTargetPath).WorkloadV1alpha1().SyncTargets().Get(ctx,
					syncerFixture.SyncerConfig.SyncTargetName,
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.GetName())

				t.Logf("Creating cowboy timothy")
				var cowboy *wildwestv1alpha1.Cowboy
				require.Eventually(t, func() bool {
					cowboy, err = servers[sourceClusterName].client.Cowboys(testNamespace).Create(ctx, &wildwestv1alpha1.Cowboy{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "timothy",
							Namespace: testNamespace,
							Labels: map[string]string{
								"state.workload.kcp.io/" + syncTargetKey: string(workloadv1alpha1.ResourceStateSync),
							},
						},
						Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("err: %v", err)
					}

					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "expected cowboy resource to be created")

				nsLocator := shared.NewNamespaceLocator(logicalcluster.From(cowboy), logicalcluster.From(syncTarget), syncTarget.GetUID(), syncTarget.GetName(), cowboy.Namespace)
				targetNamespace, err := shared.PhysicalClusterNamespaceName(nsLocator)
				require.NoError(t, err, "Error determining namespace mapping for %v", nsLocator)

				t.Logf("Expecting namespace %s to show up in sink", targetNamespace)
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
				framework.Eventually(t, func() (bool, string) {
					if got, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							cowboy, err := servers[sourceClusterName].client.Cowboys(testNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
							if err != nil {
								return false, fmt.Sprintf("error getting cowboy %q: %v", cowboy.Name, err)
							}
							return false, "Downstream cowboy couldn't be found."
						}
						return false, fmt.Sprintf("error getting cowboy %q in sink: %v", cowboy.Name, err)
					} else if diff := cmp.Diff(cowboy.Spec, got.Spec); diff != "" {
						return false, fmt.Sprintf("spec mismatch (-want +got):\n%s", diff)
					}
					return true, ""
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
						t.Logf("Error getting cowboy %q in source: %v", cowboy.Name, err)
						return false
					} else if diff := cmp.Diff(updated.Status, got.Status); diff != "" {
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "expected source to show status change")

				t.Logf("Deleting object in the source")
				err = servers[sourceClusterName].client.Cowboys(testNamespace).Delete(ctx, cowboy.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "error deleting source cowboy")

				// TODO(ncdc): the expect code for cowboys currently expects the cowboy to exist. See if we can adjust it
				// so we can reuse that here instead of polling.
				t.Logf("Expecting the object in the sink to be deleted")
				require.Eventually(t, func() bool {
					_, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected sink cowboy to be deleted")
			},
		},
	}

	source := framework.SharedKcpServer(t)
	orgPath, _ := framework.NewOrganizationFixture(t, source, framework.TODO_WithoutMultiShardSupport())

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			t.Log("Creating a workspace")
			wsPath, _ := framework.NewWorkspaceFixture(t, source, orgPath, framework.WithName("source"), framework.TODO_WithoutMultiShardSupport())

			// clients
			sourceConfig := source.BaseConfig(t)

			sourceKubeClient, err := kcpkubernetesclientset.NewForConfig(sourceConfig)
			require.NoError(t, err)

			sourceWildwestClusterClient, err := wildwestclusterclientset.NewForConfig(sourceConfig)
			require.NoError(t, err)

			syncerFixture := framework.NewSyncerFixture(t, source, wsPath,
				framework.WithExtraResources("cowboys.wildwest.dev", "services"),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					// Always install the crd regardless of whether the target is
					// logical or not since cowboys is not a native type.
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err)
					t.Log("Installing test CRDs into sink cluster...")
					fixturewildwest.FakePClusterCreate(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
				})).Start(t)

			t.Logf("Bind second user workspace to location workspace")
			framework.NewBindCompute(t, wsPath, source).Bind(t)

			sinkWildwestClient, err := wildwestclientset.NewForConfig(syncerFixture.DownstreamConfig)
			require.NoError(t, err)

			t.Log("Creating namespace in source cluster...")
			_, err = sourceKubeClient.Cluster(wsPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			runningServers := map[string]runningServer{
				sourceClusterName: {
					client:     sourceWildwestClusterClient.Cluster(wsPath).WildwestV1alpha1(),
					coreClient: sourceKubeClient.Cluster(wsPath).CoreV1(),
				},
				sinkClusterName: {
					client:     sinkWildwestClient.WildwestV1alpha1(),
					coreClient: syncerFixture.DownstreamKubeClient.CoreV1(),
				},
			}

			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers, syncerFixture)
		})
	}
}
