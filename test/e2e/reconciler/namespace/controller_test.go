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

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/cluster"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	clusterclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/cluster/v1alpha1"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const clusterLabel = "kcp.dev/cluster"

func TestNamespaceScheduler(t *testing.T) {
	t.Parallel()

	const (
		serverName           = "main"
		physicalCluster1Name = "pcluster1"
		physicalCluster2Name = "pcluster2"
	)

	f := framework.NewKcpFixture(t,
		framework.KcpConfig{
			Name: serverName,
			Args: []string{
				"--discovery-poll-interval=2s",
				"--push-mode=true", // Required to ensure clusters are ready for scheduling
			},
		},
		// this is a kcp acting as a physical cluster
		framework.KcpConfig{
			Name: physicalCluster1Name,
			Args: []string{
				"--run-controllers=false",
			},
		},
		// this is a kcp acting as a physical cluster
		framework.KcpConfig{
			Name: physicalCluster2Name,
			Args: []string{
				"--run-controllers=false",
			},
		},
	)

	// TODO(marun) Collapse this sub test into its parent. It's only
	// left in this form to simplify review of the transition to the
	// new fixture.
	t.Run("validate namespace scheduling",
		func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			require.Equal(t, 3, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

			cfg, err := server.Config()
			require.NoError(t, err)

			kubeClient, err := kubernetes.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			apiextensionClusterClient, err := apiextensionsclient.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			orgClusterName := framework.NewOrganizationFixture(t, server)
			clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
			err = crds.Create(ctx, apiextensionClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "apiresourceimports"},
				metav1.GroupResource{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
				metav1.GroupResource{Group: cluster.GroupName, Resource: "clusters"},
			)
			require.NoError(t, err)

			client := kubeClient.Cluster(clusterName)

			clients, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			clusterClient := clients.Cluster(clusterName).ClusterV1alpha1().Clusters()

			expect, err := expectNamespaces(ctx, t, client)
			require.NoError(t, err, "failed to start expecter")

			t.Log("Create a namespace without a cluster available and expect it to be marked unschedulable")

			namespace, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "e2e-nss-",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create namespace1")

			server.Artifact(t, func() (runtime.Object, error) {
				return client.CoreV1().Namespaces().Get(ctx, namespace.Name, metav1.GetOptions{})
			})

			err = expect(namespace, unschedulableMatcher())
			require.NoError(t, err, "did not see namespace marked unschedulable")

			t.Log("Create a cluster and expect the namespace to be scheduled to it")

			server1RawConfig, err := f.Servers[physicalCluster1Name].RawConfig()
			require.NoError(t, err, "failed to get server 1 raw config")

			server1Kubeconfig, err := clientcmd.Write(server1RawConfig)
			require.NoError(t, err, "failed to marshal server 1 kubeconfig")

			cluster1, err := clusterClient.Create(ctx, &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "e2e-nss-1-",
				},
				Spec: clusterv1alpha1.ClusterSpec{
					KubeConfig: string(server1Kubeconfig),
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create cluster1")

			// Wait for the cluster to become ready. A namespace can only be assigned to a ready cluster.
			waitForClusterReadiness(t, ctx, clusterClient, cluster1.Name)

			err = expect(namespace, scheduledMatcher(cluster1.Name))
			require.NoError(t, err, "did not see namespace marked scheduled for cluster1 %q", cluster1.Name)

			t.Log("Create a new cordoned cluster")

			server2RawConfig, err := f.Servers[physicalCluster2Name].RawConfig()
			require.NoError(t, err, "failed to get server 2 raw config")

			server2Kubeconfig, err := clientcmd.Write(server2RawConfig)
			require.NoError(t, err, "failed to get server 2 kubeconfig")

			cluster2, err := clusterClient.Create(ctx, &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "e2e-nss-2-",
				},
				Spec: clusterv1alpha1.ClusterSpec{
					KubeConfig:    string(server2Kubeconfig),
					Unschedulable: true, // cordoned.
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create cluster2")

			t.Log("Delete the old cluster and expect the namespace to end up unschedulable (since the new cluster is cordoned).")

			err = clusterClient.Delete(ctx, cluster1.Name, metav1.DeleteOptions{})
			require.NoError(t, err, "failed to delete cluster1 %q", cluster1.Name)

			err = expect(namespace, unschedulableMatcher())
			require.NoError(t, err, "did not see namespace marked unschedulable")

			t.Log("Uncordon the new cluster and expect the namespace to end up scheduled to it.")

			schedulablePatchBytes := []byte(`[{"op":"replace","path":"/spec/unschedulable","value":false}]`) // uncordoned.
			cluster2, err = clusterClient.Patch(ctx, cluster2.Name, types.JSONPatchType, schedulablePatchBytes, metav1.PatchOptions{})
			require.NoError(t, err, "failed to uncordon cluster2")

			err = expect(namespace, scheduledMatcher(cluster2.Name))
			require.NoError(t, err, "did not see namespace marked scheduled for cluster2 %q", cluster2.Name)

			t.Log("Mark the new cluster for immediate eviction, and see the namespace get unschedulable.")

			anHourAgo := metav1.NewTime(time.Now().Add(-time.Hour))
			evictPatchBytes := []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/evictAfter","value":%q}]`, anHourAgo.Format(time.RFC3339)))
			cluster2, err = clusterClient.Patch(ctx, cluster2.Name, types.JSONPatchType, evictPatchBytes, metav1.PatchOptions{})
			require.NoError(t, err, "failed to evict cluster2")

			err = expect(namespace, unschedulableMatcher())
			require.NoError(t, err, "did not see namespace marked unscheablable")

			t.Log("Unevict the new cluster and see the namespace scheduled to it.")

			unevictPatchBytes := []byte(`[{"op":"replace","path":"/spec/evictAfter","value":null}]`)
			cluster2, err = clusterClient.Patch(ctx, cluster2.Name, types.JSONPatchType, unevictPatchBytes, metav1.PatchOptions{})
			require.NoError(t, err, "failed to unevict cluster2")

			waitForClusterReadiness(t, ctx, clusterClient, cluster2.Name)

			err = expect(namespace, scheduledMatcher(cluster2.Name))
			require.NoError(t, err, "did not see namespace marked scheduled for cluster2 %q", cluster2.Name)

			t.Log("Delete the remaining cluster and expect the namespace to be marked unschedulable.")

			err = clusterClient.Delete(ctx, cluster2.Name, metav1.DeleteOptions{})
			require.NoError(t, err, "failed to delete cluster2 %q", cluster2.Name)

			err = expect(namespace, unschedulableMatcher())
			require.NoError(t, err, "did not see namespace marked unschedulable")
		},
	)
}

type namespaceExpectation func(*corev1.Namespace) error

func unschedulableMatcher() namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if nscontroller.IsScheduled(object) {
			return fmt.Errorf("expected an unschedulable namespace, got cluster=%q; status.conditions: %#v", object.Labels["kcp.dev/cluster"], object.Status.Conditions)
		}
		return nil
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

func waitForClusterReadiness(t *testing.T, ctx context.Context, client clusterclient.ClusterInterface, clusterName string) {
	t.Logf("Waiting for cluster %q to become ready", clusterName)
	require.Eventually(t, func() bool {
		cluster, err := client.Get(ctx, clusterName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("Error getting cluster %q: %v", clusterName, err)
			return false
		}
		return conditions.IsTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	t.Logf("Cluster %q confirmed ready", clusterName)
}
