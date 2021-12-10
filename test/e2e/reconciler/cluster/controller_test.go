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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
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

const testNamespace = "cluster-controller-test"
const clusterName = "us-east1"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestClusterController(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client  wildwestclient.CowboyInterface
		watcher watch.Interface
		expect  RegisterCowboyExpectation
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
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer) {
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
			if err := installCrd(ctx, servers); err != nil {
				t.Error(err)
				return
			}
			t.Logf("Installed test CRDs after %s", time.Since(start))
			start = time.Now()
			source, sink := servers[sourceClusterName], servers[sinkClusterName]
			t.Log("Installing sink cluster...")
			if err := installCluster(t, ctx, source, sink); err != nil {
				t.Error(err)
				return
			}
			t.Logf("Installed sink cluster after %s", time.Since(start))
			start = time.Now()
			t.Log("Setting up clients for test...")
			if err := installNamespace(ctx, source); err != nil {
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
				clusterName, err := detectClusterName(cfg, ctx, "cowboys.wildwest.dev")
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
					"--push_mode",
					"--install_cluster_controller",
					"--resources_to_sync=cowboys.wildwest.dev",
					"--auto_publish_apis",
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

func installNamespace(ctx context.Context, server framework.RunningServer) error {
	cfg, err := server.Config()
	if err != nil {
		return err
	}
	clusterName, err := detectClusterName(cfg, ctx, "cowboys.wildwest.dev")
	if err != nil {
		return fmt.Errorf("failed to detect cluster name: %w", err)
	}
	clients, err := kubernetesclientset.NewClusterForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to construct client for server: %w", err)
	}
	client := clients.Cluster(clusterName)
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
	}, metav1.CreateOptions{})
	return err
}

func installCrd(ctx context.Context, servers map[string]framework.RunningServer) error {
	wg := sync.WaitGroup{}
	bootstrapErrChan := make(chan error, len(servers))
	for _, server := range servers {
		wg.Add(1)
		go func(server framework.RunningServer) {
			defer wg.Done()
			cfg, err := server.Config()
			if err != nil {
				bootstrapErrChan <- err
				return
			}
			crdClient, err := apiextensionsv1client.NewForConfig(cfg)
			if err != nil {
				bootstrapErrChan <- fmt.Errorf("failed to construct client for server: %w", err)
				return
			}
			bootstrapErrChan <- config.BootstrapCustomResourceDefinitionFromFS(ctx, crdClient.CustomResourceDefinitions(), metav1.GroupKind{
				Group: wildwest.GroupName,
				Kind:  "cowboys",
			}, rawCustomResourceDefinitions)
		}(server)
	}
	wg.Wait()
	close(bootstrapErrChan)
	var bootstrapErrors []error
	for err := range bootstrapErrChan {
		bootstrapErrors = append(bootstrapErrors, err)
	}
	if err := kerrors.NewAggregate(bootstrapErrors); err != nil {
		return fmt.Errorf("could not bootstrap CRDs: %w", err)
	}
	return nil
}

func installCluster(t framework.TestingTInterface, ctx context.Context, source, sink framework.RunningServer) error {
	sourceCfg, err := source.Config()
	if err != nil {
		return fmt.Errorf("failed to get source config: %w", err)
	}
	rawSinkCfg, err := sink.RawConfig()
	if err != nil {
		return fmt.Errorf("failed to get sink config: %w", err)
	}
	sourceClusterName, err := detectClusterName(sourceCfg, ctx, "clusters.cluster.example.dev")
	if err != nil {
		return fmt.Errorf("failed to detect cluster name: %w", err)
	}
	sourceKcpClients, err := kcpclientset.NewClusterForConfig(sourceCfg)
	if err != nil {
		return fmt.Errorf("failed to construct client for server: %w", err)
	}
	rawSinkCfgBytes, err := clientcmd.Write(rawSinkCfg)
	if err != nil {
		return fmt.Errorf("failed to serialize sink config: %w", err)
	}
	sourceKcpClient := sourceKcpClients.Cluster(sourceClusterName)
	cluster, err := sourceKcpClient.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec:       clusterv1alpha1.ClusterSpec{KubeConfig: string(rawSinkCfgBytes)},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create cluster on source kcp: %w", err)
	}
	defer source.Artifact(t, func() (runtime.Object, error) {
		return sourceKcpClient.ClusterV1alpha1().Clusters().Get(ctx, cluster.Name, metav1.GetOptions{})
	})
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer func() {
		cancel()
	}()
	watcher, err := sourceKcpClient.ClusterV1alpha1().Clusters().Watch(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", cluster.Name).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to watch cluster in source kcp: %w", err)
	}
	for {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("failed to wait for cluster in source kcp to be ready: %w", waitCtx.Err())
		case event := <-watcher.ResultChan():
			switch event.Type {
			case watch.Added, watch.Bookmark:
				continue
			case watch.Modified:
				updated, ok := event.Object.(*clusterv1alpha1.Cluster)
				if !ok {
					continue
				}
				var ready bool
				for _, condition := range updated.Status.Conditions {
					if condition.Type == clusterv1alpha1.ClusterConditionReady && condition.Status == corev1.ConditionTrue {
						ready = true
						break
					}
				}
				if ready {
					return nil
				}
			case watch.Deleted:
				return fmt.Errorf("cluster %s was deleted before being ready", cluster.Name)
			case watch.Error:
				return fmt.Errorf("encountered error while watching cluster %s: %#v", cluster.Name, event.Object)
			}
		}
	}
}

// TODO: we need to undo the prefixing and get normal sharding behavior in soon ... ?
func detectClusterName(cfg *rest.Config, ctx context.Context, crdName string) (string, error) {
	crdClient, err := apiextensionsclientset.NewClusterForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to construct client for server: %w", err)
	}
	crds, err := crdClient.Cluster("*").ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list crds: %w", err)
	}
	if len(crds.Items) == 0 {
		return "", errors.New("found no crds, cannot detect cluster name")
	}
	for _, crd := range crds.Items {
		if crd.ObjectMeta.Name == crdName {
			return crd.ObjectMeta.ClusterName, nil
		}
	}
	return "", errors.New("detected no admin cluster")
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
