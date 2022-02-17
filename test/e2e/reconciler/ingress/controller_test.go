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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/informers"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	networkingclient "k8s.io/client-go/kubernetes/typed/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

//go:embed *.yaml
var embeddedResources embed.FS

const crdName = "ingresses.networking.k8s.io"
const testNamespace = "ingress-controller-test"
const clusterName = "us-east1"
const existingServiceName = "existing-service"
const sourceClusterName, sinkClusterName = "source", "sink"

func TestIngressController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		client networkingclient.NetworkingV1Interface
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, servers map[string]runningServer)
	}{
		{
			name: "ingress lifecycle",
			work: func(ctx context.Context, t *testing.T, servers map[string]runningServer) {
				t.Logf("Creating ingress in source cluster")
				ingressYaml, err := embeddedResources.ReadFile("ingress.yaml")
				require.NoError(t, err, "failed to read ingress")
				var ingress *v1.Ingress
				err = yaml.Unmarshal(ingressYaml, &ingress)
				require.NoError(t, err, "failed to unmarshal ingress")
				ingress, err = servers[sourceClusterName].client.Ingresses(testNamespace).Create(ctx, ingress, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ingress")

				t.Logf("Waiting for ingress to be synced to sink cluster")
				nsLocator := syncer.NamespaceLocator{LogicalCluster: ingress.ClusterName, Namespace: ingress.Namespace}
				targetNamespace, err := syncer.PhysicalClusterNamespaceName(nsLocator)
				require.NoError(t, err, "error determining namespace mapping for %v", nsLocator)
				expectedIngress := ingress.DeepCopy()
				expectedIngress.GenerateName = ingress.Name + "-"
				expectedIngress.Namespace = targetNamespace
				require.Eventually(t, func() bool {
					ingress, err := servers[sinkClusterName].client.Ingresses(targetNamespace).Get(ctx, expectedIngress.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}
					framework.RequireNoDiff(t, ingress.Spec, expectedIngress.Spec)
					return true
				}, wait.ForeverTestTimeout, time.Second, "did not see the ingress synced on sink cluster")

				t.Logf("Deleting ingress in source cluster")
				err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
					ingress, err := servers[sourceClusterName].client.Ingresses(testNamespace).Get(ctx, ingress.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					ingress.Spec.Rules[0].Host = "valid-ingress-2.kcp-apps.127.0.0.1.nip.io"
					_, err = servers[sourceClusterName].client.Ingresses(testNamespace).Update(ctx, ingress, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed updating the ingress object in the source cluster")

				t.Logf("Waiting for ingress update to be synced to sink cluster")
				require.Eventually(t, func() bool {
					ingress, err := servers[sinkClusterName].client.Ingresses(targetNamespace).Get(ctx, expectedIngress.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}
					return ingress.Spec.Rules[0].Host == "valid-ingress-2.kcp-apps.127.0.0.1.nip.io"
				}, wait.ForeverTestTimeout, time.Second, "did not see Ingress spec updated on sink cluster")
			},
		},
	}
	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// TODO(marun) Refactor tests to enable the use of shared fixture
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: "source",
					Args: []string{
						"--push-mode",
						"--auto-publish-apis=true",
						"--resources-to-sync=ingresses.networking.k8s.io,deployments.apps,services",
					},
				},
				framework.KcpConfig{
					Name: "sink",
					Args: []string{
						"--run-controllers=false",
					},
				},
			)
			source, sink := f.Servers[sourceClusterName], f.Servers[sinkClusterName]

			start := time.Now()
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			require.Equal(t, 2, len(f.Servers), "incorrect number of servers")

			orgClusterName := framework.NewOrganizationFixture(t, source)
			wsClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

			// clients
			sourceConfig, err := source.Config()
			require.NoError(t, err)
			sourceKubeClusterClient, err := kubernetesclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceCrdClusterClient, err := apiextensionsclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)
			sourceKcpClusterClient, err := kcpclientset.NewClusterForConfig(sourceConfig)
			require.NoError(t, err)

			sourceCrdClient := sourceCrdClusterClient.Cluster(wsClusterName)
			sourceKubeClient := sourceKubeClusterClient.Cluster(wsClusterName)

			sinkConfig, err := sink.Config()
			require.NoError(t, err)
			sinkKubeClient, err := kubernetesclientset.NewForConfig(sinkConfig)
			require.NoError(t, err)

			t.Log("Installing test CRDs into source cluster...")
			err = configcrds.Create(ctx, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: clusterapi.GroupName, Resource: "clusters"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "apiresourceimports"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
			)
			require.NoError(t, err)
			err = configcrds.CreateFromFS(ctx, sourceCrdClient.ApiextensionsV1().CustomResourceDefinitions(), embeddedResources,
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
				metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)

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

			t.Log("Creating service in source cluster...")
			_, err = sourceKubeClient.CoreV1().Services(testNamespace).Create(ctx, &corev1.Service{
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
			require.NoError(t, err, "failed to install service in source cluster")

			runningServers := map[string]runningServer{
				sourceClusterName: {
					RunningServer: f.Servers[sourceClusterName],
					client:        sourceKubeClient.NetworkingV1(),
				},
				sinkClusterName: {
					RunningServer: f.Servers[sinkClusterName],
					client:        sinkKubeClient.NetworkingV1(),
				},
			}

			t.Log("Starting ingress-controller...")
			envoyListenerPort, err := framework.GetFreePort(t)
			require.NoError(t, err, "failed to pick envoy listener port")
			xdsListenerPort, err := framework.GetFreePort(t)
			require.NoError(t, err, "failed to pick xds listener port")
			artifactDir, err := framework.CreateTempDirForTest(t, "artifacts")
			require.NoError(t, err, "failed to create artifact dir for ingress-controller")
			kubeconfigPath := filepath.Join(artifactDir, "ingress-controller.kubeconfig")
			adminConfig, err := source.RawConfig()
			require.NoError(t, err)

			ingressConfig := clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{
					"ingress-workspace": adminConfig.Clusters["system:admin"],
				},
				Contexts: map[string]*clientcmdapi.Context{
					"ingress-workspace": {
						Cluster:  "ingress-workspace",
						AuthInfo: "admin",
					},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"admin": adminConfig.AuthInfos["admin"],
				},
				CurrentContext: "ingress-workspace",
			}
			ingressConfig.Clusters["ingress-workspace"].Server += "/clusters/" + sourceClusterName

			err = clientcmd.WriteToFile(ingressConfig, kubeconfigPath)
			require.NoError(t, err, "failed to write kubeconfig to file")
			ingressController := framework.NewAccessory(t, artifactDir,
				"ingress-controller", // name
				"ingress-controller",
				"--kubeconfig="+kubeconfigPath,
				"--envoy-listener-port="+envoyListenerPort,
				"--envoy-xds-port="+xdsListenerPort,
			)
			err = ingressController.Run(ctx)
			require.NoError(t, err, "failed to start ingress controller")

			t.Log("Starting test...")
			testCase.work(ctx, t, runningServers)
		})
	}
}

// RegisterIngressExpectation registers an expectation about the future state of the seed.
type RegisterIngressExpectation func(seed *v1.Ingress, expectation IngressExpectation) error

// IngressExpectation evaluates an expectation about the object.
type IngressExpectation func(ingress *v1.Ingress) error

// ExpectIngresses sets up an Expecter in order to allow registering expectations in tests with minimal setup.
func ExpectIngresses(ctx context.Context, t *testing.T, client kubernetesclientset.Interface) (RegisterIngressExpectation, error) {
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
				if all[i].Namespace == seed.Namespace && all[i].GenerateName == seed.GenerateName {
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
