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

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

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
