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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	workloadnamespace "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestNamespaceScheduler(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	type runningServer struct {
		framework.RunningServer
		clusterName    logicalcluster.Name
		client         kubernetes.Interface
		kcpClient      clientset.Interface
		expect         registerNamespaceExpectation
		orgClusterName logicalcluster.Name
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
				err = server.expect(namespace, unscheduledMatcher(workloadnamespace.NamespaceReasonUnschedulable))
				require.NoError(t, err, "did not see namespace marked unschedulable")

				t.Log("Deploy a syncer")
				// Create and Start a syncer against a workload cluster so that there's a ready cluster to schedule to.
				//
				// TODO(marun) Extract the heartbeater out of the syncer for reuse in a test fixture. The namespace
				// controller just needs ready clusters which can be accomplished without a syncer by having the
				// heartbeater update the sync target so the heartbeat controller can set the cluster ready.
				syncerFixture := framework.NewSyncerFixture(t, server, server.clusterName,
					framework.WithExtraResources("services")).Start(t)
				syncTargetName := syncerFixture.SyncerConfig.SyncTargetName

				t.Logf("Bind to location workspace")
				framework.NewBindCompute(t, server.clusterName, server).Bind(t)

				t.Log("Wait for \"kubernetes\" apiexport")
				require.Eventually(t, func() bool {
					_, err := server.kcpClient.ApisV1alpha1().APIExports().Get(ctx, "kubernetes", metav1.GetOptions{})
					return err == nil
				}, wait.ForeverTestTimeout, time.Millisecond*100)

				t.Log("Wait for \"kubernetes\" apibinding that is bound")
				require.Eventually(t, func() bool {
					binding, err := server.kcpClient.ApisV1alpha1().APIBindings().Get(ctx, "kubernetes", metav1.GetOptions{})
					if err != nil {
						t.Log(err)
						return false
					}
					return binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncerFixture.SyncerConfig.SyncTargetWorkspace, syncerFixture.SyncerConfig.SyncTargetName)

				t.Log("Wait until the namespace is scheduled to the workload cluster")
				require.Eventually(t, func() bool {
					ns, err := server.client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
					if err != nil {
						t.Log(err)
						return false
					}
					return scheduledMatcher(syncTargetKey)(ns) == nil
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Log("Cordon the cluster and expect the namespace to end up unschedulable")
				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					cluster, err := server.kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
					if err != nil {
						return err
					}
					anHourAgo := metav1.NewTime(time.Now().Add(-1 * time.Hour))
					cluster.Spec.EvictAfter = &anHourAgo
					_, err = server.kcpClient.WorkloadV1alpha1().SyncTargets().Update(ctx, cluster, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update cluster1")

				err = server.expect(namespace, unscheduledMatcher(workloadnamespace.NamespaceReasonUnschedulable))
				require.NoError(t, err, "did not see namespace marked unschededuled")
			},
		},
		{
			name: "GVRs are removed, and then quickly re-added to a new workspace",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err, "failed to construct apiextensions client for server")

				dynamicClusterClient, err := kcpdynamic.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err, "failed to construct dynamic client for server")

				kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
				require.NoError(t, err, "failed to construct kubernetes client for server")

				t.Log("Create a ready SyncTarget, and keep it artificially ready") // we don't want the syncer to do anything with CRDs, hence we fake the syncer
				cluster := &workloadv1alpha1.SyncTarget{
					ObjectMeta: metav1.ObjectMeta{Name: "cluster7"},
					Spec: workloadv1alpha1.SyncTargetSpec{
						SupportedAPIExports: []apisv1alpha1.BindingReference{
							{
								Export: &apisv1alpha1.ExportBindingReference{
									Name: "kubernetes",
									Path: server.clusterName.String(),
								},
							},
						},
					},
				}
				cluster, err = server.kcpClient.WorkloadV1alpha1().SyncTargets().Create(ctx, cluster, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create cluster")
				syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(cluster), cluster.Name)

				go wait.UntilWithContext(ctx, func(ctx context.Context) {
					patchBytes := []byte(fmt.Sprintf(`[{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, time.Now().Format(time.RFC3339)))
					_, err := server.kcpClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, cluster.Name, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
					if err != nil {
						// we can survive several of these errors. If 6 in a row fail and the sync target is marked
						// non-ready, we likely have other problems than these failures here.
						t.Logf("failed to set status.lastSyncerHeartbeatTime: %v", err)
						return
					}
				}, 100*time.Millisecond)

				t.Logf("Bind to location workspace")
				framework.NewBindCompute(t, server.clusterName, server,
					framework.WithAPIExportsWorkloadBindOption(server.clusterName.String()+":kubernetes"),
				).Bind(t)

				t.Log("Create a new unique sheriff CRD")
				group := framework.UniqueGroup(".io")
				crd := newSheriffCRD(group)
				gvr := schema.GroupVersionResource{
					Group:    crd.Spec.Group,
					Version:  crd.Spec.Versions[0].Name,
					Resource: crd.Spec.Names.Plural,
				}
				err = configcrds.CreateSingle(ctx, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(server.clusterName), crd)
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
						t.Logf("failed to get sheriff: %v", err)
						return false
					}
					return obj.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see sheriff scheduled")

				t.Log("Delete the sheriff and the sheriff CRD")
				err = dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Delete(ctx, "woody", metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete sheriff")
				err = crdClusterClient.Cluster(server.clusterName).ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, crd.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete CRD")

				time.Sleep(7 * time.Second) // this must be longer than discovery repoll interval (5s in tests)

				t.Log("Recreate the CRD, and then quickly a namespace and a CR whose CRD was just recreated")
				err = configcrds.CreateSingle(ctx, crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(server.clusterName), crd)
				require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, server.clusterName)
				_, err = kubeClusterClient.Cluster(server.clusterName).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "namespace-test"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace")
				_, err = dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Create(ctx, newSheriff(group, "lucky-luke"), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create sheriff")

				t.Log("Now also the sheriff should be scheduled")
				require.Eventually(t, func() bool {
					obj, err := dynamicClusterClient.Cluster(server.clusterName).Resource(gvr).Namespace("default").Get(ctx, "lucky-luke", metav1.GetOptions{})
					if err != nil {
						t.Logf("failed to get sheriff: %v", err)
						return false
					}
					return obj.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey] != ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to see sheriff scheduled")
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

			cfg := server.BaseConfig(t)

			clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName)

			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err)

			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err)

			expecterClient, err := kcpkubernetesclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
			require.NoError(t, err)

			t.Logf("Starting namespace expecter")
			expect, err := expectNamespaces(ctx, t, expecterClient)
			require.NoError(t, err, "failed to start expecter")

			s := runningServer{
				RunningServer:  server,
				clusterName:    clusterName,
				client:         kubeClusterClient.Cluster(clusterName),
				kcpClient:      kcpClusterClient.Cluster(clusterName),
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
		if condition := conditions.Get(&workloadnamespace.NamespaceConditionsAdapter{Namespace: object}, workloadnamespace.NamespaceScheduled); condition != nil {
			if condition.Status == corev1.ConditionTrue {
				return fmt.Errorf("expected an unscheduled namespace, got: %#v", object.Status.Conditions)
			}
			if condition.Reason != reason {
				return fmt.Errorf("expected an unscheduled namespace with reason %s, got status.conditions: %#v", reason, object.Status.Conditions)
			}
		}
		return nil
	}
}

func scheduledMatcher(target string) namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if _, found := object.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+target]; found {
			return nil
		}
		return fmt.Errorf("expected a scheduled namespace, got status.conditions: %#v", object.Status.Conditions)
	}
}

type registerNamespaceExpectation func(seed *corev1.Namespace, expectation namespaceExpectation) error

func expectNamespaces(ctx context.Context, t *testing.T, client kcpkubernetesclientset.ClusterInterface) (registerNamespaceExpectation, error) {
	informerFactory := kcpkubernetesinformers.NewSharedInformerFactory(client, 0)
	informer := informerFactory.Core().V1().Namespaces()
	expecter := framework.NewExpecter(informer.Informer())
	informerFactory.Start(ctx.Done())
	if !cache.WaitForNamedCacheSync(t.Name(), ctx.Done(), informer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *corev1.Namespace, expectation namespaceExpectation) error {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(seed)
		if err != nil {
			return err
		}
		clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
		if err != nil {
			return err
		}
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			current, err := informer.Lister().Cluster(clusterName).Get(name)
			if err != nil {
				// Retry on all errors
				return false, err
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, wait.ForeverTestTimeout)
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
