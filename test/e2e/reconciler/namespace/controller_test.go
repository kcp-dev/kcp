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

package namespace

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const clusterLabel = nscontroller.ClusterLabel

func TestNamespaceScheduler(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		clusterName    logicalcluster.LogicalCluster
		client         kubernetes.Interface
		orgKcpClient   versioned.Interface
		kcpClient      clientset.Interface
		expect         registerNamespaceExpectation
		orgClusterName logicalcluster.LogicalCluster
	}

	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "validate namespace scheduling",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Log("Create a namespace without a cluster available and expect it to be marked unschedulable")
				namespace, err := server.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						GenerateName: "e2e-nss-",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace1")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
				})

				err = server.expect(namespace, unscheduledMatcher(nscontroller.NamespaceReasonUnschedulable))
				require.NoError(t, err, "did not see namespace marked unschedulable")

				// Create and Start a syncer against a workload cluster so that theres a ready cluster to schedule to.
				//
				// TODO(marun) Extract the heartbeater out of the syncer for reuse in a test fixture. The namespace
				// controller just needs ready clusters which can be accomplished without a syncer by having the
				// heartbeater update the workload cluster so the heartbeat controller can set the cluster ready.
				syncerFixture := framework.SyncerFixture{
					UpstreamServer:       server,
					WorkspaceClusterName: server.clusterName,
				}.Start(t)
				workloadClusterName := syncerFixture.SyncerConfig.WorkloadClusterName
				err = server.expect(namespace, scheduledMatcher(workloadClusterName))
				require.NoError(t, err, "did not see namespace marked scheduled for cluster1 %q", workloadClusterName)

				t.Log("Cordon the cluster and expect the namespace to end up unscheduled")

				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					cluster, err := server.kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx, workloadClusterName, metav1.GetOptions{})
					if err != nil {
						return err
					}
					anHourAgo := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					cluster.Spec.EvictAfter = &anHourAgo
					_, err = server.kcpClient.WorkloadV1alpha1().WorkloadClusters().Update(ctx, cluster, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update cluster1")

				err = server.expect(namespace, unscheduledMatcher(nscontroller.NamespaceReasonUnschedulable))
				require.NoError(t, err, "did not see namespace marked unschedulable")

				t.Log("Disable the scheduling for the namespace and expect it to be marked unscheduled")

				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					ns, err := server.client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					ns.Labels[nscontroller.SchedulingDisabledLabel] = ""
					_, err = server.client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update namespace")

				err = server.expect(namespace, unscheduledMatcher(nscontroller.NamespaceReasonSchedulingDisabled))
				require.NoError(t, err, "did not see namespace marked with scheduling disabled")
			},
		},
		{
			name: "new GVRs are scheduled",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				crdClusterClient, err := apiextensionsclient.NewClusterForConfig(server.DefaultConfig(t))
				require.NoError(t, err, "failed to construct apiextensions client for server")
				dynamicClusterClient, err := dynamic.NewClusterForConfig(server.DefaultConfig(t))
				require.NoError(t, err, "failed to construct dynamic client for server")
				kubeClusterClient, err := kubernetes.NewClusterForConfig(server.DefaultConfig(t))
				require.NoError(t, err, "failed to construct kubernetes client for server")

				t.Log("Create a ready WorkloadCluster, and keep it artificially ready") // we don't want the syncer to do anything with CRDs, hence we fake the syncer
				cluster := &workloadv1alpha1.WorkloadCluster{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster7"},
					Spec:       workloadv1alpha1.WorkloadClusterSpec{},
				}
				cluster, err = server.kcpClient.WorkloadV1alpha1().WorkloadClusters().Create(ctx, cluster, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create cluster")
				require.Eventually(t, func() bool {
					cluster, err = server.kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx, cluster.Name, metav1.GetOptions{})
					conditions.MarkTrue(cluster, "Ready")
					cluster, err = server.kcpClient.WorkloadV1alpha1().WorkloadClusters().UpdateStatus(ctx, cluster, metav1.UpdateOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				go wait.UntilWithContext(ctx, func(ctx context.Context) {
					patchBytes := []byte(fmt.Sprintf(`[{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, time.Now().Format(time.RFC3339)))
					_, err := server.kcpClient.WorkloadV1alpha1().WorkloadClusters().Patch(ctx, cluster.Name, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
					if err != nil {
						klog.Errorf("failed to set status.lastSyncerHeartbeatTime: %v", err)
						return
					}
				}, time.Second*10)

				t.Log("Create a new unique sheriff CRD")
				group := uuid.New().String() + ".io"
				crd := newSheriffCRD(group)
				gvr := schema.GroupVersionResource{Group: crd.Spec.Group, Resource: "sheriffs", Version: "v1"}
				err = configcrds.CreateSingle(ctx, crdClusterClient.Cluster(server.clusterName).ApiextensionsV1().CustomResourceDefinitions(), crd)
				require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, server.clusterName)
				require.Eventually(t, func() bool {
					_, err := dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see CRD in cluster")

				t.Log("Create a sheriff and wait for it to be scheduled")
				_, err = dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Create(ctx, newSheriff(group, "woody"), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create sheriff")
				require.Eventually(t, func() bool {
					obj, err := dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Get(ctx, "woody", metav1.GetOptions{})
					if err != nil {
						return false
					}
					require.NoError(t, err, "failed to get sheriff")
					return obj.GetLabels()[clusterLabel] != ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see sheriff scheduled")

				t.Log("Delete the sheriff and the sheriff CRD")
				err = dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Delete(ctx, "woody", metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete sheriff")
				err = crdClusterClient.Cluster(server.clusterName).ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, crd.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete CRD")

				time.Sleep(30 * time.Second)

				t.Log("Recreate the CRD, and then quickly a namespace, a configmap and a CR whose CRD was just recreated")
				err = configcrds.CreateSingle(ctx, crdClusterClient.Cluster(server.clusterName).ApiextensionsV1().CustomResourceDefinitions(), crd)
				require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, server.clusterName)
				_, err = kubeClusterClient.Cluster(server.clusterName).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "namespace-test"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace")
				_, err = dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Create(ctx, newSheriff(group, "lucky-luke"), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create sheriff")

				t.Log("Create a configmap and wait for it to be scheduled")
				_, err = kubeClusterClient.Cluster(server.clusterName).CoreV1().ConfigMaps("namespace-test").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "configmap-test"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create configmap")
				require.Eventually(t, func() bool {
					obj, err := kubeClusterClient.Cluster(server.clusterName).CoreV1().ConfigMaps("namespace-test").Get(ctx, "configmap-test", metav1.GetOptions{})
					if err != nil {
						klog.Errorf("failed to get configmap: %v", err)
						return false
					}
					return obj.GetLabels()[clusterLabel] != ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see configmap scheduled")

				t.Log("Now also the sheriff should be scheduled")
				require.Eventually(t, func() bool {
					obj, err := dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Get(ctx, "lucky-luke", metav1.GetOptions{})
					if err != nil {
						klog.Errorf("failed to get sheriff: %v", err)
						return false
					}
					return obj.GetLabels()[clusterLabel] != ""
				}, time.Second*10, time.Millisecond*100, "failed to see sheriff scheduled")
			},
		},
	}

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)

	for i := range testCases {
		testCase := testCases[i]

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			cfg := server.DefaultConfig(t)

			kubeClient, err := kubernetes.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

			client := kubeClient.Cluster(clusterName)

			clients, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			kcpClient := clients.Cluster(clusterName)
			orgKcpClient := clients.Cluster(orgClusterName)

			t.Logf("Starting namespace expecter")
			expect, err := expectNamespaces(ctx, t, client)
			require.NoError(t, err, "failed to start expecter")

			s := runningServer{
				RunningServer:  server,
				clusterName:    clusterName,
				client:         client,
				orgKcpClient:   orgKcpClient,
				kcpClient:      kcpClient,
				expect:         expect,
				orgClusterName: orgClusterName,
			}

			t.Logf("Set up clients for test after %s", time.Since(start))
			t.Log("Starting test...")

			testCase.work(ctx, t, s)
		},
		)
	}
}

type namespaceExpectation func(*corev1.Namespace) error

func unscheduledMatcher(reason string) namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if condition := conditions.Get(&nscontroller.NamespaceConditionsAdapter{Namespace: object}, nscontroller.NamespaceScheduled); condition != nil {
			if condition.Status == corev1.ConditionTrue {
				return fmt.Errorf("expected an unscheduled namespace, got cluster=%q; status.conditions: %#v", object.Labels[clusterLabel], object.Status.Conditions)
			}
			if condition.Reason != reason {
				return fmt.Errorf("expected an unscheduled namespace with reason %s, got status.conditions: %#v", reason, object.Status.Conditions)
			}
			if object.Labels[clusterLabel] != "" {
				return fmt.Errorf("expected cluster assignment to be empty, got %q", object.Labels[clusterLabel])
			}
			return nil
		} else {
			return fmt.Errorf("expected an unscheduled namespace, missing scheduled condition: %#v", object.Status.Conditions)
		}
	}
}

func scheduledMatcher(target string) namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if !nscontroller.IsScheduled(object) {
			return fmt.Errorf("expected a scheduled namespace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Labels[clusterLabel] != target {
			return fmt.Errorf("expected namespace assignment to be %q, got %q", target, object.Labels[clusterLabel])
		}
		return nil
	}
}

type registerNamespaceExpectation func(seed *corev1.Namespace, expectation namespaceExpectation) error

func expectNamespaces(ctx context.Context, t *testing.T, client kubernetes.Interface) (registerNamespaceExpectation, error) {
	informerFactory := informers.NewSharedInformerFactory(client, 0)
	informer := informerFactory.Core().V1().Namespaces()
	expecter := framework.NewExpecter(informer.Informer())
	informerFactory.Start(ctx.Done())
	if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), informer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *corev1.Namespace, expectation namespaceExpectation) error {
		key, err := cache.MetaNamespaceKeyFunc(seed)
		if err != nil {
			return err
		}
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			current, err := informer.Lister().Get(key)
			if err != nil {
				// Retry on all errors
				return false, err
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, 30*time.Second)
	}, nil
}

func newSheriffCRD(group string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("sheriffs.%s", group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "sheriffs",
				Singular: "sheriff",
				Kind:     "Sheriff",
				ListKind: "SheriffList",
			},
			Scope: "Namespaced",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}
}

func newSheriff(group string, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": group + "/v1",
			"kind":       "Sheriff",
			"metadata": map[string]interface{}{
				"name": name,
			},
		},
	}
}
