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

package syncer

import (
	"context"
	"embed"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	coreexternalversions "k8s.io/client-go/informers"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned"
	wildwestexternalversions "github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/informers/externalversions"
)

func init() {
	utilruntime.Must(wildwestv1alpha1.AddToScheme(scheme.Scheme))
}

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

const crdName = "cowboys.wildwest.dev"
const clusterName = "us-east1"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestSpecSyncer(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		wildwestClientSet wildwestclientset.Interface
		k8sClientSet      kubernetesclientset.Interface
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer)
	}{
		{
			name: "create a resource in new namespace, expect ns name to contain lcluster + ns name and for resource to get synced to correct ns",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {
				const testNamespace = "syncer-test"
				expectedSinkNsName := fmt.Sprintf("kcp--admin--%s", testNamespace)

				sourceNsClient := servers[sourceClusterName].k8sClientSet.CoreV1().Namespaces()
				sourceCowboyClient := servers[sourceClusterName].wildwestClientSet.WildwestV1alpha1().Cowboys(testNamespace)
				sinkNsClient := servers[sinkClusterName].k8sClientSet.CoreV1().Namespaces()
				sinkWildwestClient := servers[sinkClusterName].wildwestClientSet.WildwestV1alpha1()

				coreSharedInformerFactory := coreexternalversions.NewSharedInformerFactoryWithOptions(servers[sinkClusterName].k8sClientSet, 0)
				nsInformer := coreSharedInformerFactory.Core().V1().Namespaces()
				nsExpecter := framework.NewExpecter(nsInformer.Informer())
				sinkNsLister := nsInformer.Lister()
				coreSharedInformerFactory.Start(ctx.Done())

				wildwestSharedInformerFactory := wildwestexternalversions.NewSharedInformerFactoryWithOptions(servers[sinkClusterName].wildwestClientSet, 0)
				cowboyInformer := wildwestSharedInformerFactory.Wildwest().V1alpha1().Cowboys()
				cowboyExpecter := framework.NewExpecter(cowboyInformer.Informer())
				sinkCowboyLister := cowboyInformer.Lister().Cowboys(expectedSinkNsName)
				wildwestSharedInformerFactory.Start(ctx.Done())

				ns, err := sourceNsClient.Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNamespace,
					},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}

				cowboy, err := sourceCowboyClient.Create(ctx, &wildwestv1alpha1.Cowboy{
					ObjectMeta: metav1.ObjectMeta{
						Name: "timothy",
					},
					Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create cowboy: %v", err)
					return
				}

				if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), nsInformer.Informer().HasSynced) {
					t.Error("failed to wait for namespace cache to sync")
					return
				}

				if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), cowboyInformer.Informer().HasSynced) {
					t.Error("failed to wait for cowboy cache to sync")
					return
				}

				defer servers[sourceClusterName].Artifact(t, func() (runtime.Object, error) {
					return sourceNsClient.Get(ctx, ns.Name, metav1.GetOptions{})
				})
				defer servers[sourceClusterName].Artifact(t, func() (runtime.Object, error) {
					return sourceCowboyClient.Get(ctx, cowboy.Name, metav1.GetOptions{})
				})
				defer servers[sinkClusterName].Artifact(t, func() (runtime.Object, error) {
					return sinkNsClient.Get(ctx, fmt.Sprintf("kcp--admin--%s", ns.Name), metav1.GetOptions{})
				})
				defer servers[sinkClusterName].Artifact(t, func() (runtime.Object, error) {
					return sinkWildwestClient.Cowboys(fmt.Sprintf("kcp--admin--%s", cowboy.Name)).Get(ctx, cowboy.GetName(), metav1.GetOptions{})
				})

				if err := nsExpecter.ExpectBefore(ctx, func(ctx context.Context) (bool, error) {
					sinkNsList, err := sinkNsLister.List(labels.Everything())
					if err != nil {
						return true, err
					}
					var names []string
					for _, ns := range sinkNsList {
						names = append(names, ns.Name)
						if ns.Name == expectedSinkNsName {
							return true, nil
						}
					}
					return false, fmt.Errorf("No namespace in sink cluster: %v", names)
				}, 30*time.Second); err != nil {
					t.Errorf("did not see namespace on sink cluster: %v", err)
					return
				}

				if err := cowboyExpecter.ExpectBefore(ctx, func(ctx context.Context) (bool, error) {
					sinkCowboyList, err := sinkCowboyLister.List(labels.Everything())
					if err != nil {
						return true, err
					}

					if len(sinkCowboyList) != 1 {
						return false, fmt.Errorf("Expected 1 cowboy, but found %d", len(sinkCowboyList))
					}

					return true, nil
				}, 30*time.Second); err != nil {
					t.Errorf("did not see cowboy on sink cluster: %v", err)
					return
				}
			},
		},
	}
	for i := range testCases {
		testCase := testCases[i]
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
			if len(servers) != 2 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			start := time.Now()
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

				kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
				if err != nil {
					t.Errorf("failed to construct client for server: %v", err)
					return
				}
				kubeClient := kubeClients.Cluster(clusterName)

				wildwestClients, err := wildwestclientset.NewClusterForConfig(cfg)
				if err != nil {
					t.Errorf("failed to construct client for server: %v", err)
					return
				}
				wildwestClient := wildwestClients.Cluster(clusterName)

				runningServers[name] = runningServer{
					RunningServer:     servers[name],
					wildwestClientSet: wildwestClient,
					k8sClientSet:      kubeClient,
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
					"--install-namespace-scheduler",
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
