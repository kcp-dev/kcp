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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"

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
		client wildwestclient.CowboyInterface
		expect RegisterCowboyExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer)
	}{
		{
			name: "create an object, expect spec to sync to sink",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {
				cowboy, err := servers[sourceClusterName].client.Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "timothy",
						Labels: map[string]string{
							"kcp.dev/cluster": clusterName,
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create cowboy: %v", err)
					return
				}
				for _, name := range []string{sourceClusterName, sinkClusterName} {
					defer servers[name].Artifact(t, func() (runtime.Object, error) {
						return servers[name].client.Get(ctx, cowboy.Name, metav1.GetOptions{})
					})
				}
				if err := servers[sinkClusterName].expect(cowboy, func(object *wildwestv1alpha1.Cowboy) error {
					if diff := cmp.Diff(cowboy.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see cowboy spec updated on sink cluster: %v", err)
					return
				}
			},
		},
		{
			name: "update a synced object, expect status to sync to source",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {
				cowboy, err := servers[sourceClusterName].client.Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "timothy",
						Labels: map[string]string{
							"kcp.dev/cluster": clusterName,
						},
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create cowboy: %v", err)
					return
				}
				for _, name := range []string{sourceClusterName, sinkClusterName} {
					defer servers[name].Artifact(t, func() (runtime.Object, error) {
						return servers[name].client.Get(ctx, cowboy.Name, metav1.GetOptions{})
					})
				}
				if err := servers[sinkClusterName].expect(cowboy, func(object *wildwestv1alpha1.Cowboy) error {
					// just wait for the sink the catch up
					if diff := cmp.Diff(cowboy.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see cowboy status updated on source cluster: %v", err)
					return
				}
				updated, err := servers[sinkClusterName].client.Patch(ctx, cowboy.Name, types.MergePatchType, []byte(`{"status":{"result":"giddyup"}}`), metav1.PatchOptions{}, "status")
				if err != nil {
					t.Errorf("failed to patch cowboy: %v", err)
					return
				}
				if err := servers[sourceClusterName].expect(cowboy, func(object *wildwestv1alpha1.Cowboy) error {
					if diff := cmp.Diff(updated.Status, object.Status); diff != "" {
						return fmt.Errorf("saw incorrect status on source cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see cowboy status updated on source cluster: %v", err)
					return
				}
			},
		},
	}
	for i := range testCases {
		testCase := testCases[i]
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
			start := time.Now()
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}
			if len(servers) != 2 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			t.Log("Installing test CRDs...")
			if err := framework.InstallCrd(ctx, metav1.GroupKind{Group: wildwest.GroupName, Kind: "cowboys"}, servers, rawCustomResourceDefinitions); err != nil {
				t.Error(err)
				return
			}
			t.Logf("Installed test CRDs after %s", time.Since(start))
			start = time.Now()
			source, sink := servers[sourceClusterName], servers[sinkClusterName]
			t.Log("Installing sink cluster...")
			if err := framework.InstallCluster(t, ctx, source, sink, "clusters.cluster.example.dev", clusterName); err != nil {
				t.Error(err)
				return
			}
			t.Logf("Installed sink cluster after %s", time.Since(start))
			start = time.Now()
			t.Log("Setting up clients for test...")
			if err := framework.InstallNamespace(ctx, source, crdName, testNamespace); err != nil {
				t.Error(err)
				return
			}
			runningServers := map[string]runningServer{}
			for _, name := range []string{sourceClusterName, sinkClusterName} {
				cfg, err := servers[name].Config()
				if err != nil {
					t.Error(err)
					return
				}
				clusterName, err := framework.DetectClusterName(cfg, ctx, crdName)
				if err != nil {
					t.Errorf("failed to detect cluster name: %v", err)
					return
				}
				wildwestClients, err := wildwestclientset.NewClusterForConfig(cfg)
				if err != nil {
					t.Errorf("failed to construct client for server: %v", err)
					return
				}
				wildwestClient := wildwestClients.Cluster(clusterName)
				expect, err := ExpectCowboys(ctx, t, wildwestClient)
				if err != nil {
					t.Errorf("failed to start expecter: %v", err)
					return
				}
				runningServers[name] = runningServer{
					RunningServer: servers[name],
					client:        wildwestClient.WildwestV1alpha1().Cowboys(testNamespace),
					expect:        expect,
				}
			}
			t.Logf("Set up clients for test after %s", time.Since(start))
			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		},
			// this is the host kcp cluster from which we sync spec
			framework.KcpConfig{
				Name: sourceClusterName,
				Args: []string{
					"--push-mode",
					"--install-cluster-controller",
					"--resources-to-sync=cowboys.wildwest.dev",
					"--auto-publish-apis",
				},
			},
			// this is a kcp acting as a target cluster to sync status from
			framework.KcpConfig{
				Name: sinkClusterName,
				Args: []string{},
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
