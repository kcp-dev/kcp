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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned"
	wildwestclient "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned/typed/wildwest/v1alpha1"
	wildwestexternalversions "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/informers/externalversions"
)

func init() {
	utilruntime.Must(wildwestv1alpha1.AddToScheme(scheme.Scheme))
}

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

const crdName = "cowboys.wildwest.dev"
const testNamespace = "cluster-controller-test"
const clusterName = "us-east1"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestClusterController(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client     wildwestclient.WildwestV1alpha1Interface
		coreClient corev1client.CoreV1Interface
		expect     RegisterCowboyExpectation
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
							"kcp.dev/cluster": clusterName,
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				require.NoErrorf(t, err, "failed to create cowboy: %v", err)

				nsLocator := syncer.NamespaceLocator{LogicalCluster: cowboy.ClusterName, Namespace: cowboy.Namespace}
				targetNamespace, err := syncer.PhysicalClusterNamespaceName(nsLocator)
				t.Logf("Expecting namespace %s to show up in sink", targetNamespace)
				require.NoErrorf(t, err, "Error determining namespace mapping for %v: %v", nsLocator, err)
				require.Eventually(t, func() bool {
					if _, err = servers[sinkClusterName].coreClient.Namespaces().Get(ctx, targetNamespace, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							return false
						}
						require.NoErrorf(t, err, "Error getting namespace %v: %v", targetNamespace, err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				defer servers[sourceClusterName].Artifact(t, func() (runtime.Object, error) {
					return servers[sourceClusterName].client.Cowboys(testNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
				})
				defer servers[sinkClusterName].Artifact(t, func() (runtime.Object, error) {
					return servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
				})

				t.Logf("Expecting same spec to show up in sink")
				cowboy.SetNamespace(targetNamespace)
				err = servers[sinkClusterName].expect(cowboy, func(object *wildwestv1alpha1.Cowboy) error {
					if diff := cmp.Diff(cowboy.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				})
				require.NoErrorf(t, err, "did not see cowboy spec updated on sink cluster: %v", err)

				t.Logf("Patching status in sink")
				updated, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Patch(ctx, cowboy.Name, types.MergePatchType, []byte(`{"status":{"result":"giddyup"}}`), metav1.PatchOptions{}, "status")
				require.NoError(t, err, "failed to patch cowboy: %v", err)

				t.Logf("Expecting status update to show up in source")
				cowboy.SetNamespace(testNamespace)
				err = servers[sourceClusterName].expect(cowboy, func(object *wildwestv1alpha1.Cowboy) error {
					if diff := cmp.Diff(updated.Status, object.Status); diff != "" {
						return fmt.Errorf("saw incorrect status on source cluster: %s", diff)
					}
					return nil
				})
				require.NoErrorf(t, err, "did not see cowboy status updated on source cluster: %v", err)

				err = servers[sourceClusterName].client.Cowboys(testNamespace).Delete(ctx, cowboy.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "error deleting source cowboy")

				// TODO(ncdc): the expect code for cowboys currently expects the cowboy to exist. See if we can adjust it
				// so we can reuse that here instead of polling.
				require.Eventually(t, func() bool {
					_, err := servers[sinkClusterName].client.Cowboys(targetNamespace).Get(ctx, cowboy.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, 30*time.Second, 100*time.Millisecond, "expected sink cowboy to be deleted")
			},
		},
	}

	f := framework.NewKCPFixture(
		// this is the host kcp cluster from which we sync spec
		framework.KcpConfig{
			Name: sourceClusterName,
			Args: []string{
				"--push-mode",
				"--run-controllers=false", "--unsupported-run-individual-controllers=cluster",
				"--resources-to-sync=cowboys.wildwest.dev",
				"--auto-publish-apis",
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
	f.SetUp(t)

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
			require.Equalf(t, len(f.Servers), 2, "incorrect number of servers")

			t.Log("Installing test CRDs...")
			err := framework.InstallCrd(ctx, metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"}, f.Servers, rawCustomResourceDefinitions)
			require.NoError(t, err)

			t.Logf("Installed test CRDs after %s", time.Since(start))
			start = time.Now()
			source, sink := f.Servers[sourceClusterName], f.Servers[sinkClusterName]
			t.Log("Installing sink cluster...")
			// TODO(marun) Use raw *testing.T
			wrappedT := framework.NewT(ctx, t)
			err = framework.InstallCluster(wrappedT, ctx, source, sink, "clusters.cluster.example.dev", clusterName)
			require.NoError(t, err)

			t.Logf("Installed sink cluster after %s", time.Since(start))
			start = time.Now()
			t.Log("Setting up clients for test...")
			err = framework.InstallNamespace(ctx, source, crdName, testNamespace)
			require.NoError(t, err)

			runningServers := map[string]runningServer{}
			for _, name := range []string{sourceClusterName, sinkClusterName} {
				cfg, err := f.Servers[name].Config()
				require.NoError(t, err)

				clusterName, err := framework.DetectClusterName(cfg, ctx, crdName)
				require.NoErrorf(t, err, "failed to detect cluster name: %v", err)

				wildwestClients, err := wildwestclientset.NewClusterForConfig(cfg)
				require.NoErrorf(t, err, "failed to construct client for server: %v", err)

				wildwestClient := wildwestClients.Cluster(clusterName)
				expect, err := ExpectCowboys(ctx, t, wildwestClient)
				require.NoErrorf(t, err, "failed to start expecter: %v", err)

				coreClients, err := kubernetes.NewClusterForConfig(cfg)
				require.NoErrorf(t, err, "failed to construct client for server: %v", err)

				coreClient := coreClients.Cluster(clusterName)

				runningServers[name] = runningServer{
					RunningServer: f.Servers[name],
					client:        wildwestClient.WildwestV1alpha1(),
					coreClient:    coreClient.CoreV1(),
					expect:        expect,
				}
			}
			t.Logf("Set up clients for test after %s", time.Since(start))
			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		},
		)
	}
}

// RegisterCowboyExpectation registers an expectation about the future state of the seed.
type RegisterCowboyExpectation func(seed *wildwestv1alpha1.Cowboy, expectation CowboyExpectation) error

// CowboyExpectation evaluates an expectation about the object.
type CowboyExpectation func(*wildwestv1alpha1.Cowboy) error

// ExpectCowboys sets up an Expecter in order to allow registering expectations in tests with minimal setup.
func ExpectCowboys(ctx context.Context, t framework.TestingTInterface, client wildwestclientset.Interface) (RegisterCowboyExpectation, error) {
	sharedInformerFactory := wildwestexternalversions.NewSharedInformerFactoryWithOptions(client, 0)
	informer := sharedInformerFactory.Wildwest().V1alpha1().Cowboys()
	expecter := framework.NewExpecter(informer.Informer())
	sharedInformerFactory.Start(ctx.Done())
	if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), informer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *wildwestv1alpha1.Cowboy, expectation CowboyExpectation) error {
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			// we are using a seed from one kcp to expect something about an object in
			// another kcp, so the cluster names will not match - this is fine, just do
			// client-side filtering for what we know
			all, err := informer.Lister().Cowboys(seed.Namespace).List(labels.Everything())
			if err != nil {
				return !apierrors.IsNotFound(err), err
			}
			var current *wildwestv1alpha1.Cowboy
			for i := range all {
				if all[i].Namespace == seed.Namespace && all[i].Name == seed.Name {
					current = all[i]
				}
			}
			if current == nil {
				return false, apierrors.NewNotFound(schema.GroupResource{
					Group:    wildwest.GroupName,
					Resource: "cowboys",
				}, seed.Name)
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, 30*time.Second)
	}, nil
}
