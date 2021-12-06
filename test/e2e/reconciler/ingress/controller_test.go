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

package workspace

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	networkingclient "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/config"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

const testNamespace = "ingress-controller-test"
const clusterName = "us-east1"
const existingServiceName = "existing-service"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestIngressController(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client networkingclient.IngressInterface
		expect RegisterIngressExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer)
	}{
		{
			name: "create an ingress that points to a valid service and expect the Ingress to be scheduled to the same cluster",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {

				ingressYaml, err := rawCustomResourceDefinitions.ReadFile("ingress.yaml")
				if err != nil {
					t.Errorf("failed to read ingress: %v", err)
					return
				}

				var ingress *v1.Ingress
				if err := yaml.Unmarshal(ingressYaml, &ingress); err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				ingress, err = servers[sourceClusterName].client.Create(ctx, ingress, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				ingress.Name = ingress.Name + "--" + clusterName
				if err := servers[sinkClusterName].expect(ingress, func(object *v1.Ingress) error {
					if diff := cmp.Diff(ingress.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see Ingress spec updated on sink cluster: %v", err)
					return
				}
			},
		},
		{
			name: "update the ingress expect the sink to be updated",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {
				ingressYaml, err := rawCustomResourceDefinitions.ReadFile("ingress.yaml")
				if err != nil {
					t.Errorf("failed to read ingress: %v", err)
					return
				}

				var ingress *v1.Ingress
				if err := yaml.Unmarshal(ingressYaml, &ingress); err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				ingress, err = servers[sourceClusterName].client.Create(ctx, ingress, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				expectedIngress := ingress.DeepCopy()
				expectedIngress.Name = expectedIngress.Name + "--" + clusterName
				if err := servers[sinkClusterName].expect(expectedIngress, func(object *v1.Ingress) error {
					if diff := cmp.Diff(expectedIngress.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see the ingress synced on sink cluster: %v", err)
					return
				}

				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					ingress, err := servers[sourceClusterName].client.Get(ctx, ingress.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					ingress.Spec.Rules[0].Host = "valid-ingress-2.kcp-apps.127.0.0.1.nip.io"
					_, err = servers[sourceClusterName].client.Update(ctx, ingress, metav1.UpdateOptions{})
					if err != nil {
						return err
					}
					return nil
				})

				if err != nil {
					t.Errorf("failed updating the ingress object in the source cluster: %v", err)
					return
				}

				ingress.Name = ingress.Name + "--" + clusterName
				if err := servers[sinkClusterName].expect(ingress, func(object *v1.Ingress) error {
					if diff := cmp.Diff(ingress.Spec, object.Spec); diff != "" {
						return fmt.Errorf("saw incorrect spec on sink cluster: %s", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see Ingress spec updated on sink cluster: %v", err)
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
			requiredCrds := []metav1.GroupKind{
				{Group: "core.k8s.io", Kind: "services"},
				{Group: "apps.k8s.io", Kind: "deployments"},
				{Group: "networking.k8s.io", Kind: "ingresses"},
			}
			for _, requiredCrd := range requiredCrds {
				if err := installCrd(ctx, requiredCrd, servers); err != nil {
					t.Error(err)
					return
				}
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
			if err := installNamespace(ctx, sink); err != nil {
				t.Error(err)
				return
			}

			if err := installService(ctx, source); err != nil {
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
				clusterName, err := detectClusterName(cfg, ctx, "ingresses.networking.k8s.io")
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
				expect, err := ExpectIngresses(ctx, t, kubeClient)
				if err != nil {
					t.Errorf("failed to start expecter: %v", err)
					return
				}
				runningServers[name] = runningServer{
					RunningServer: servers[name],
					client:        kubeClient.NetworkingV1().Ingresses(testNamespace),
					expect:        expect,
				}
			}

			cfg, err := source.RawConfig()
			if err != nil {
				return
			}

			envoyListenerPort, err := framework.GetFreePort(t)
			if err != nil {
				t.Error(err)
				return

			}
			xdsListenerPort, err := framework.GetFreePort(t)
			if err != nil {
				t.Error(err)
				return
			}

			ingressController := ingressControllerConfig{
				t:               t,
				kubeconfigPath:  cfg.Clusters[cfg.CurrentContext].LocationOfOrigin,
				artifactDir:     t.TempDir(),
				xdsListenPort:   xdsListenerPort,
				envoyListenPort: envoyListenerPort,
			}

			err = ingressController.Run(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			t.Logf("Set up clients for test after %s", time.Since(start))
			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		},
			framework.KcpConfig{
				Name: "source",
				Args: []string{
					"--push-mode",
					"--install-cluster-controller",
					"--auto-publish-apis=true",
					"--resources-to-sync=ingresses.networking.k8s.io,deployments.apps,services"},
			},
			framework.KcpConfig{
				Name: "sink",
				Args: []string{},
			},
		)
	}
}

type ingressControllerConfig struct {
	t               framework.TestingTInterface
	kubeconfigPath  string
	ctx             context.Context
	artifactDir     string
	xdsListenPort   string
	envoyListenPort string
}

func (c *ingressControllerConfig) Run(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)

	if deadline, ok := c.t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
		ctx = deadlinedCtx
		c.t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	c.ctx = ctx
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	c.t.Cleanup(func() {
		c.t.Log("cleanup: ending ingress controller")
		cancel()
		<-cleanupCtx.Done()
	})

	cmd := exec.CommandContext(c.ctx, "ingress-controller", []string{
		"-kubeconfig=" + c.kubeconfigPath,
		"-envoyxds",
		"-envoy-listener-port=" + c.envoyListenPort,
		"-envoyxds-port=" + c.xdsListenPort,
	}...)

	c.t.Logf("running: %v", strings.Join(cmd.Args, " "))
	logFile, err := os.Create(filepath.Join(c.artifactDir, "ingress-controller.log"))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
	log := bytes.Buffer{}
	writers := []io.Writer{&log, logFile}
	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw
	if err := cmd.Start(); err != nil {
		cleanupCancel()
		return err
	}
	go func() {
		defer func() { cleanupCancel() }()
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			c.t.Errorf("`ingress-controller` failed: %w output: %s", err, log)
		}
	}()
	return nil
}

func installService(ctx context.Context, server framework.RunningServer) error {
	cfg, err := server.Config()
	if err != nil {
		return err
	}
	sourceClusterName, err := detectClusterName(cfg, ctx, "ingresses.networking.k8s.io")
	if err != nil {
		return fmt.Errorf("failed to detect cluster name: %w", err)
	}
	clients, err := kubernetesclientset.NewClusterForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to construct client for server: %w", err)
	}
	client := clients.Cluster(sourceClusterName)
	_, err = client.CoreV1().Services(testNamespace).Create(ctx, &corev1.Service{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name: existingServiceName,
			Labels: map[string]string{
				"kcp.dev/cluster": clusterName,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     "http",
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": existingServiceName,
			},
		},
		Status: corev1.ServiceStatus{},
	}, metav1.CreateOptions{})
	return err
}

func installNamespace(ctx context.Context, server framework.RunningServer) error {
	cfg, err := server.Config()
	if err != nil {
		return err
	}
	clusterName, err := detectClusterName(cfg, ctx, "ingresses.networking.k8s.io")
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

func installCrd(ctx context.Context, gvk metav1.GroupKind, servers map[string]framework.RunningServer) error {
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
			bootstrapErrChan <- config.BootstrapCustomResourceDefinitionFromFS(ctx, crdClient.CustomResourceDefinitions(), gvk, rawCustomResourceDefinitions)
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

// RegisterIngressExpectation registers an expectation about the future state of the seed.
type RegisterIngressExpectation func(seed *v1.Ingress, expectation IngressExpectation) error

// IngressExpectation evaluates an expectation about the object.
type IngressExpectation func(ingress *v1.Ingress) error

// ExpectIngresses sets up an Expecter in order to allow registering expectations in tests with minimal setup.
func ExpectIngresses(ctx context.Context, t framework.TestingTInterface, client kubernetesclientset.Interface) (RegisterIngressExpectation, error) {
	sharedInformerFactory := informers.NewSharedInformerFactory(client, 0)
	informer := sharedInformerFactory.Networking().V1().Ingresses()
	expecter := framework.NewExpecter(informer.Informer())
	sharedInformerFactory.Start(ctx.Done())
	if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), informer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *v1.Ingress, expectation IngressExpectation) error {
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			// we are using a seed from one kcp to expect something about an object in
			// another kcp, so the cluster names will not match - this is fine, just do
			// client-side filtering for what we know
			all, err := informer.Lister().Ingresses(seed.Namespace).List(labels.Everything())
			if err != nil {
				return !apierrors.IsNotFound(err), err
			}
			var current *v1.Ingress
			for i := range all {
				if all[i].Namespace == seed.Namespace && all[i].Name == seed.Name {
					current = all[i]
				}
			}
			if current == nil {
				return false, apierrors.NewNotFound(schema.GroupResource{
					Group:    v1.GroupName,
					Resource: "ingress",
				}, seed.Name)
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, 30*time.Second)
	}, nil
}
