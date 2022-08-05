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
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclient "k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
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
		client     wildwestclient.WildwestV1alpha1Interface
		coreClient corev1client.CoreV1Interface
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, servers map[string]runningServer, syncerFixture *framework.StartedSyncerFixture)
	}{
		{
			name: "create an object, expect spec and status to sync to sink, then delete",
			work: func(ctx context.Context, t *testing.T, servers map[string]runningServer, syncerFixture *framework.StartedSyncerFixture) {
				kcpClient, err := kcpclientset.NewForConfig(syncerFixture.SyncerConfig.UpstreamConfig)
				require.NoError(t, err)

				syncTarget, err := kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx,
					syncerFixture.SyncerConfig.SyncTargetName,
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.GetName())

				t.Logf("Creating cowboy timothy")
				cowboy, err := servers[sourceClusterName].client.Cowboys(testNamespace).Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "timothy",
						Labels: map[string]string{
							"state.workload.kcp.dev/" + syncTargetKey: string(workloadv1alpha1.ResourceStateSync),
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create cowboy")

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
							return false, fmt.Sprintf("Downstream cowboy couldn't be found. Here is the upstream cowboy:\n%s", toYaml(t, cowboy))
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
			wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

			// clients
			sourceConfig := source.BaseConfig(t)
			sourceWsClusterConfig := kcpclienthelper.ConfigWithCluster(sourceConfig, wsClusterName)

			sourceKubeClient, err := kubernetesclient.NewForConfig(sourceWsClusterConfig)
			require.NoError(t, err)
			sourceWildwestClient, err := wildwestclientset.NewForConfig(sourceWsClusterConfig)
			require.NoError(t, err)

			syncerFixture := framework.NewSyncerFixture(t, source, wsClusterName,
				framework.WithExtraResources("cowboys.wildwest.dev"),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					// Always install the crd regardless of whether the target is
					// logical or not since cowboys is not a native type.
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err)
					t.Log("Installing test CRDs into sink cluster...")
					fixturewildwest.Create(t, logicalcluster.Name{}, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
				})).Start(t)

			sinkWildwestClient, err := wildwestclientset.NewForConfig(syncerFixture.DownstreamConfig)
			require.NoError(t, err)

			t.Log("Creating namespace in source cluster...")
			_, err = sourceKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			runningServers := map[string]runningServer{
				sourceClusterName: {
					client:     sourceWildwestClient.WildwestV1alpha1(),
					coreClient: sourceKubeClient.CoreV1(),
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

func toYaml(t *testing.T, obj interface{}) string {
	bytes, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(bytes)
}
