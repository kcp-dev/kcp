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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const clusterLabel = "kcp.dev/cluster"

func TestNamespaceScheduler(t *testing.T) {
	// TODO(jasonhall): Make tests pass.
	t.Skip("Tests currently don't pass")

	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface)
	}{
		{
			name: "create a namespace without clusters, expect it to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface) {
				namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(namespace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
			},
		},
		{
			name: "add a cluster after a namespace is unschedulable, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface) {
				namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(namespace), 5*time.Second); err != nil {
					t.Errorf("did not see namespace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
				bostonCluster, err := client.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create cluster: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonCluster.Name), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
			},
		},
		{
			name: "create a namespace with a cluster, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface) {
				bostonCluster, err := client.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first cluster: %v", err)
					return
				}
				namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(namespace), 5*time.Second); err != nil {
					t.Errorf("did not see namespace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonCluster.Name), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
			},
		},
		{
			name: "delete a cluster that a namespace is scheduled to, expect it to move to another cluster",
			work: func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface) {
				bostonCluster, err := client.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first cluster: %v", err)
					return
				}
				atlantaCluster, err := client.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "atlanta"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create second cluster: %v", err)
					return
				}
				namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(namespace), 5*time.Second); err != nil {
					t.Errorf("did not see namespace created: %v", err)
					return
				}
				namespace, err = expectNextEvent(watcher, watch.Modified, scheduledAnywhereMatcher(), 5*time.Second)
				if err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
				var otherCluster string
				for _, name := range []string{bostonCluster.Name, atlantaCluster.Name} {
					if name != namespace.Labels[clusterLabel] {
						otherCluster = name
						break
					}
				}
				if err := client.ClusterV1alpha1().Clusters().Delete(ctx, namespace.Labels[clusterLabel], metav1.DeleteOptions{}); err != nil {
					t.Errorf("failed to delete cluster: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(otherCluster), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
		{
			name: "delete all clusters, expect namespace to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, kubeClient kubernetes.Interface, client clientset.Interface, watcher watch.Interface) {
				bostonCluster, err := client.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first cluster: %v", err)
					return
				}
				namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create namespace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(namespace), 5*time.Second); err != nil {
					t.Errorf("did not see namespace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonCluster.Name), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
				if err := client.ClusterV1alpha1().Clusters().Delete(ctx, bostonCluster.Name, metav1.DeleteOptions{}); err != nil {
					t.Errorf("failed to delete cluster: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see namespace updated: %v", err)
					return
				}
			},
		},
	}
	const serverName = "main"
	for i := range testCases {
		testCase := testCases[i]
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
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
			clusterName, err := framework.DetectClusterName(cfg, ctx, "workspaces.tenancy.kcp.dev")
			if err != nil {
				t.Errorf("failed to detect cluster name: %v", err)
				return
			}
			kubeClient, err := kubernetes.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			client := kubeClient.Cluster(clusterName)
			watcher, err := client.CoreV1().Namespaces().Watch(ctx, metav1.ListOptions{})
			if err != nil {
				t.Errorf("failed to watch namespaces: %v", err)
				return
			}

			clients, err := clientset.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			clusterClient := clients.Cluster(clusterName)

			testCase.work(ctx, t, client, clusterClient, watcher)
		}, framework.KcpConfig{
			Name: "main",
			Args: []string{"--install-cluster-controller", "--install-workspace-scheduler", "--install-namespace-scheduler"},
		})
	}
}

func exactMatcher(expected *corev1.Namespace) matcher {
	return func(object *corev1.Namespace) error {
		if !apiequality.Semantic.DeepEqual(expected, object) {
			return fmt.Errorf("incorrect object: %v", cmp.Diff(expected, object))
		}
		return nil
	}
}

func unschedulableMatcher() matcher {
	return func(object *corev1.Namespace) error {
		if !isNamespaceUnschedulable(object) {
			return fmt.Errorf("expected an unschedulable namespace, got status.conditions: %#v", object.Status.Conditions)
		}
		return nil
	}
}

func scheduledMatcher(target string) matcher {
	return func(object *corev1.Namespace) error {
		if isNamespaceUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Labels[clusterLabel] != target {
			return fmt.Errorf("expected namespace assignment to be %q, got %q", target, object.Labels[clusterLabel])
		}
		return nil
	}
}

func scheduledAnywhereMatcher() matcher {
	return func(object *corev1.Namespace) error {
		if isNamespaceUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		return nil
	}
}

type matcher func(object *corev1.Namespace) error

func expectNextEvent(w watch.Interface, expectType watch.EventType, matcher matcher, duration time.Duration) (*corev1.Namespace, error) {
	event, err := nextEvent(w, duration)
	if err != nil {
		return nil, err
	}
	if expectType != event.Type {
		return nil, fmt.Errorf("got incorrect watch event type: %v != %v\n", expectType, event.Type)
	}
	workspace, ok := event.Object.(*corev1.Namespace)
	if !ok {
		return nil, fmt.Errorf("got %T, not a Workspace", event.Object)
	}
	if err := matcher(workspace); err != nil {
		return workspace, err
	}
	return workspace, nil
}

func nextEvent(w watch.Interface, duration time.Duration) (watch.Event, error) {
	stopTimer := time.NewTimer(duration)
	defer stopTimer.Stop()
	select {
	case event, ok := <-w.ResultChan():
		if !ok {
			return watch.Event{}, errors.New("watch closed unexpectedly")
		}
		return event, nil
	case <-stopTimer.C:
		return watch.Event{}, errors.New("timed out waiting for event")
	}
}

const conditionTypeUnschedulable = "Unschedulable"

func isNamespaceUnschedulable(ns *corev1.Namespace) bool {
	for _, cond := range ns.Status.Conditions {
		if cond.Type == conditionTypeUnschedulable {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
