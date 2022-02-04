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

package namespace

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const clusterLabel = "kcp.dev/cluster"

func TestNamespaceScheduler(t *testing.T) {
	const serverName = "main"
	framework.RunParallel(t, "validate namespace scheduling", func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
		ctx := context.Background()
		if deadline, ok := t.Deadline(); ok {
			withDeadline, cancel := context.WithDeadline(ctx, deadline)
			t.Cleanup(cancel)
			ctx = withDeadline
		}

		if len(servers) != 1 {
			t.Errorf("incorrect number of servers: %d", len(servers))
			return
		}
		server := servers[serverName]
		cfg, err := server.Config()
		if err != nil {
			t.Error(err)
			return
		}

		kubeClient, err := kubernetes.NewClusterForConfig(cfg)
		if err != nil {
			t.Errorf("failed to construct client for server: %v", err)
			return
		}

		clusterName, err := framework.DetectClusterName(cfg, ctx, "workspaces.tenancy.kcp.dev")
		if err != nil {
			t.Errorf("failed to detect cluster name: %v", err)
			return
		}
		client := kubeClient.Cluster(clusterName)

		clients, err := clientset.NewClusterForConfig(cfg)
		if err != nil {
			t.Errorf("failed to construct client for server: %v", err)
			return
		}
		clusterClient := clients.Cluster(clusterName).ClusterV1alpha1().Clusters()

		expect, err := expectNamespaces(ctx, t, client)
		if err != nil {
			t.Errorf("failed to start expecter: %v", err)
			return
		}

		// Create a namespace without a cluster available and expect it to be marked unscheduled

		namespace, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-nss-",
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("failed to create namespace1: %v", err)
			return
		}

		if err := expect(namespace, unschedulableMatcher()); err != nil {
			t.Errorf("did not see namespace marked unschedulable: %v", err)
			return
		}

		// Create a cluster and expect the namespace to be scheduled to it

		cluster1, err := clusterClient.Create(ctx, &clusterv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-nss-1-",
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("failed to create cluster1: %v", err)
			return
		}

		if err := expect(namespace, scheduledMatcher(cluster1.Name)); err != nil {
			t.Errorf("did not see namespace marked scheduled for cluster1 %q: %v", cluster1.Name, err)
			return
		}

		// Create a new cluster, delete the old cluster, and expect the
		// namespace to end up scheduled to the new cluster.

		cluster2, err := clusterClient.Create(ctx, &clusterv1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-nss-2-",
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("failed to create cluster2: %v", err)
			return
		}

		err = clusterClient.Delete(ctx, cluster1.Name, metav1.DeleteOptions{})
		if err != nil {
			t.Errorf("failed to delete cluster1 %q: %v", cluster1.Name, err)
			return
		}

		if err := expect(namespace, scheduledMatcher(cluster2.Name)); err != nil {
			t.Errorf("did not see namespace marked scheduled for cluster2 %q: %v", cluster2.Name, err)
			return
		}

		// Delete the remaining cluster and expect the namespace to be
		// marked unscheduled.

		err = clusterClient.Delete(ctx, cluster2.Name, metav1.DeleteOptions{})
		if err != nil {
			t.Errorf("failed to delete cluster2 %q: %v", cluster2.Name, err)
			return
		}

		if err := expect(namespace, unschedulableMatcher()); err != nil {
			t.Errorf("did not see namespace marked unschedulable: %v", err)
			return
		}
	}, framework.KcpConfig{
		Name: serverName,
		Args: []string{"--install-cluster-controller", "--install-workspace-scheduler", "--install-namespace-scheduler", "--discovery-poll-interval=2s"},
	})
}

type namespaceExpectation func(*corev1.Namespace) error

func unschedulableMatcher() namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if nscontroller.IsScheduled(object) {
			return fmt.Errorf("expected an unschedulable namespace, got status.conditions: %#v", object.Status.Conditions)
		}
		return nil
	}
}

func scheduledMatcher(target string) namespaceExpectation {
	return func(object *corev1.Namespace) error {
		if !nscontroller.IsScheduled(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Labels[clusterLabel] != target {
			return fmt.Errorf("expected namespace assignment to be %q, got %q", target, object.Labels[clusterLabel])
		}
		return nil
	}
}

type registerNamespaceExpectation func(seed *corev1.Namespace, expectation namespaceExpectation) error

func expectNamespaces(ctx context.Context, t framework.TestingTInterface, client kubernetes.Interface) (registerNamespaceExpectation, error) {
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
