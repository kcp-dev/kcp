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

package cowboy

import (
	"context"
	"embed"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned"
	wildwestclient "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned/typed/wildwest/v1alpha1"
)

func init() {
	utilruntime.Must(wildwestv1alpha1.AddToScheme(scheme.Scheme))
}

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

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
							"kcp.dev/cluster": sinkClusterName,
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create cowboy")

				nsLocator := syncer.NamespaceLocator{LogicalCluster: cowboy.ClusterName, Namespace: cowboy.Namespace}
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
						require.NoError(t, err, "Error getting cowboy %q in sink", cowboy.Name)

						return false
					} else if diff := cmp.Diff(cowboy.Spec, got.Spec); diff != "" {
						require.Errorf(t, nil, "Spec mismatch on sink cluster: %s", diff)
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
						require.NoError(t, err, "Error getting cowboy %q in source", cowboy.Name)
						return false
					} else if diff := cmp.Diff(updated.Status, got.Status); diff != "" {
						require.Errorf(t, nil, "Status mismatch on source cluster: %s", diff)
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

	f := framework.NewKcpFixture(t,
		// this is the host kcp cluster from which we sync spec
		framework.KcpConfig{
			Name: sourceClusterName,
			Args: []string{
				"--push-mode",
				"--resources-to-sync=cowboys.wildwest.dev",
				"--auto-publish-apis",
				"--discovery-poll-interval=5s",
			},
		},
		// this is a kcp acting as a target cluster to sync status from
		framework.KcpConfig{
			Name: sinkClusterName,
			Args: []string{
				"--run-controllers=false",
			},
		},
	)
	orgClusterName := framework.NewOrganizationFixture(t, f.Servers[sourceClusterName])

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}
			require.Equal(t, len(f.Servers), 2, "incorrect number of servers")

			source, sink := f.Servers[sourceClusterName], f.Servers[sinkClusterName]

			t.Log("Creating a workspace")
			wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

			// clients
			sourceConfig, err := source.Config("system:admin")
			require.NoError(t, err)
			sourceKubeClusterClient, err := kubernetesclient.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceCrdClusterClient, err := apiextensionsclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceKcpClusterClient, err := kcpclient.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceWildwestClusterClient, err := wildwestclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)

			sourceCrdClient := sourceCrdClusterClient.Cluster(wsClusterName)
			sourceKubeClient := sourceKubeClusterClient.Cluster(wsClusterName)
			sourceWildwestClient := sourceWildwestClusterClient.Cluster(wsClusterName)

			sinkConfig, err := sink.Config("system:admin")
			require.NoError(t, err)
			sinkKubeClient, err := kubernetesclient.NewForConfig(sinkConfig)
			require.NoError(t, err)
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(sinkConfig)
			require.NoError(t, err)
			sinkWildwestClient, err := wildwestclientset.NewForConfig(sinkConfig)
			require.NoError(t, err)

			t.Log("Installing test CRDs into source cluster...")
			err = configcrds.Create(ctx, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: clusterapi.GroupName, Resource: "clusters"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "apiresourceimports"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
			)
			require.NoError(t, err)
			err = configcrds.CreateFromFS(ctx, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(), rawCustomResourceDefinitions, metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
			require.NoError(t, err)

			t.Log("Installing test CRDs into sink cluster...")
			err = configcrds.CreateFromFS(ctx, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(), rawCustomResourceDefinitions, metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})
			require.NoError(t, err)
			t.Logf("Installed test CRDs after %s", time.Since(start))

			t.Log("Installing sink cluster...")
			start = time.Now()
			_, err = framework.CreateClusterAndWait(t, ctx, source.Artifact, sourceKcpClusterClient.Cluster(wsClusterName), sink)
			require.NoError(t, err)
			t.Logf("Installed sink cluster after %s", time.Since(start))

			t.Log("Creating namespace in source cluster...")
			_, err = sourceKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			runningServers := map[string]runningServer{
				sourceClusterName: {
					RunningServer: f.Servers[sourceClusterName],
					client:        sourceWildwestClient.WildwestV1alpha1(),
					coreClient:    sourceKubeClient.CoreV1(),
				},
				sinkClusterName: {
					RunningServer: f.Servers[sinkClusterName],
					client:        sinkWildwestClient.WildwestV1alpha1(),
					coreClient:    sinkKubeClient.CoreV1(),
				},
			}

			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		})
	}
}
