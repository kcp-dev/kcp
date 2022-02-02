//go:build e2e
// +build e2e

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
	"context"
	"embed"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/informers"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	networkingclient "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

const crdName = "ingresses.networking.k8s.io"
const testNamespace = "ingress-controller-test"
const clusterName = "us-east1"
const existingServiceName = "existing-service"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestIngressController(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client networkingclient.NetworkingV1Interface
		expect RegisterIngressExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer)
	}{
		{
			name: "create an ingress that points to a valid service and expect the Ingress to be scheduled to the same cluster",
			work: func(ctx context.Context, t framework.TestingTInterface, servers map[string]runningServer) {

				ingressYaml, err := embeddedResources.ReadFile("ingress.yaml")
				if err != nil {
					t.Errorf("failed to read ingress: %v", err)
					return
				}

				var ingress *v1.Ingress
				if err := yaml.Unmarshal(ingressYaml, &ingress); err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				ingress, err = servers[sourceClusterName].client.Ingresses(testNamespace).Create(ctx, ingress, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				targetNamespace := fmt.Sprintf("kcp--%s--%s", ingress.GetClusterName(), ingress.GetNamespace())
				expectedIngress := ingress.DeepCopy()
				expectedIngress.Name = ingress.Name + "--" + clusterName
				expectedIngress.SetNamespace(targetNamespace)
				if err := servers[sinkClusterName].expect(expectedIngress, func(object *v1.Ingress) error {
					if diff := cmp.Diff(expectedIngress.Spec, object.Spec); diff != "" {
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
				ingressYaml, err := embeddedResources.ReadFile("ingress.yaml")
				if err != nil {
					t.Errorf("failed to read ingress: %v", err)
					return
				}

				var ingress *v1.Ingress
				if err := yaml.Unmarshal(ingressYaml, &ingress); err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				ingress, err = servers[sourceClusterName].client.Ingresses(testNamespace).Create(ctx, ingress, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create ingress: %v", err)
					return
				}

				expectedIngress := ingress.DeepCopy()
				targetNamespace := fmt.Sprintf("kcp--%s--%s", expectedIngress.GetClusterName(), expectedIngress.GetNamespace())
				expectedIngress.Name = expectedIngress.Name + "--" + clusterName
				expectedIngress.Namespace = targetNamespace
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
					c := servers[sourceClusterName].client.Ingresses(testNamespace)
					ingress, err := c.Get(ctx, ingress.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					ingress.Spec.Rules[0].Host = "valid-ingress-2.kcp-apps.127.0.0.1.nip.io"
					_, err = c.Update(ctx, ingress, metav1.UpdateOptions{})
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
				ingress.Namespace = targetNamespace
				if err := servers[sinkClusterName].expect(ingress, func(object *v1.Ingress) error {
					if ingress.Spec.Rules[0].Host != object.Spec.Rules[0].Host {
						return fmt.Errorf("saw incorrect spec on sink cluster, expected host %s, got %s", ingress.Spec.Rules[0].Host, object.Spec.Rules[0].Host)
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
			requiredCrds := []metav1.GroupKind{
				{Group: "core.k8s.io", Kind: "services"},
				{Group: "apps.k8s.io", Kind: "deployments"},
				{Group: "networking.k8s.io", Kind: "ingresses"},
			}
			for _, requiredCrd := range requiredCrds {
				if err := framework.InstallCrd(ctx, requiredCrd, servers, embeddedResources); err != nil {
					t.Error(err)
					return
				}
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
				expect, err := ExpectIngresses(ctx, t, kubeClient)
				if err != nil {
					t.Errorf("failed to start expecter: %v", err)
					return
				}
				runningServers[name] = runningServer{
					RunningServer: servers[name],
					client:        kubeClient.NetworkingV1(),
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

			ingressController := framework.NewAccessory(t, artifactDir,
				"ingress-controller",
				"-kubeconfig="+cfg.Clusters[cfg.CurrentContext].LocationOfOrigin,
				"-envoyxds",
				"-envoy-listener-port="+envoyListenerPort,
				"-envoyxds-port="+xdsListenerPort,
			)

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

func installService(ctx context.Context, server framework.RunningServer) error {
	client, err := framework.GetClientForServer(ctx, server, crdName)
	if err != nil {
		return err
	}
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
