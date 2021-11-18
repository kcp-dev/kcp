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

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/helpers/conditions"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceController(t *testing.T) {
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface)
	}{
		{
			name: "create a workspace without shards, expect it to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface) {
				workspace, err := client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}, Status: tenancyv1alpha1.WorkspaceStatus{}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(workspace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
		{
			name: "add a shard after a workspace is unschedulable, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface) {
				workspace, err := client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(workspace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
				bostonShard, err := client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace shard: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonShard.Name), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
		{
			name: "create a workspace with a shard, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface) {
				bostonShard, err := client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				workspace, err := client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(workspace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonShard.Name), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
		{
			name: "delete a shard that a workspace is scheduled to, expect it to move to another shard",
			work: func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface) {
				bostonShard, err := client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				atlantaShard, err := client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "atlanta"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create second workspace shard: %v", err)
					return
				}
				workspace, err := client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(workspace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				workspace, err = expectNextEvent(watcher, watch.Modified, scheduledAnywhereMatcher(), 5*time.Second)
				if err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
				var otherShard string
				for _, name := range []string{bostonShard.Name, atlantaShard.Name} {
					if name != otherShard {
						otherShard = name
						break
					}
				}
				err = client.TenancyV1alpha1().WorkspaceShards().Delete(ctx, workspace.Status.Location.Current, metav1.DeleteOptions{})
				if err != nil {
					t.Errorf("failed to delete workspace shard: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(otherShard), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
		{
			name: "delete all shards, expect workspace to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, client clientset.Interface, watcher watch.Interface) {
				bostonShard, err := client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				workspace, err := client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Added, exactMatcher(workspace), 5*time.Second); err != nil {
					t.Errorf("did not see workspace created: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, scheduledMatcher(bostonShard.Name), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
				err = client.TenancyV1alpha1().WorkspaceShards().Delete(ctx, bostonShard.Name, metav1.DeleteOptions{})
				if err != nil {
					t.Errorf("failed to delete workspace shard: %v", err)
					return
				}
				if _, err := expectNextEvent(watcher, watch.Modified, unschedulableMatcher(), 5*time.Second); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
	}
	for _, testCase := range testCases {
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers ...framework.RunningServer) {
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
			server := servers[0]
			cfg := server.Config()
			if cfg == nil {
				t.Error("got nil config for server")
				return
			}
			clusterName, err := detectClusterName(cfg, ctx)
			if err != nil {
				t.Errorf("failed to detect cluster name: %v", err)
				return
			}
			clients, err := clientset.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			client := clients.Cluster(clusterName)
			watcher, err := client.TenancyV1alpha1().Workspaces().Watch(ctx, metav1.ListOptions{})
			if err != nil {
				t.Errorf("failed to watch workspaces: %v", err)
				return
			}
			testCase.work(ctx, t, client, watcher)
		}, []string{"--install_workspace_controller"})
	}
}

// TODO: we need to undo the prefixing and get normal sharding behavior in soon ... ?
func detectClusterName(cfg *rest.Config, ctx context.Context) (string, error) {
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
		if crd.ObjectMeta.Name == "workspaces.tenancy.kcp.dev" {
			return crd.ObjectMeta.ClusterName, nil
		}
	}
	return "", errors.New("detected no admin cluster")
}

func exactMatcher(expected *tenancyv1alpha1.Workspace) matcher {
	return func(object *tenancyv1alpha1.Workspace) error {
		if !apiequality.Semantic.DeepEqual(expected, object) {
			return fmt.Errorf("incorrect object: %v", cmp.Diff(expected, object))
		}
		return nil
	}
}

func unschedulableMatcher() matcher {
	return func(object *tenancyv1alpha1.Workspace) error {
		if !conditions.IsWorkspaceUnschedulable(object) {
			return fmt.Errorf("expected an unschedulable workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		return nil
	}
}

func scheduledMatcher(target string) matcher {
	return func(object *tenancyv1alpha1.Workspace) error {
		if conditions.IsWorkspaceUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Status.Location.Current != target {
			return fmt.Errorf("expected workspace.status.location.current to be %q, got %q", target, object.Status.Location.Current)
		}
		return nil
	}
}

func scheduledAnywhereMatcher() matcher {
	return func(object *tenancyv1alpha1.Workspace) error {
		if conditions.IsWorkspaceUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		return nil
	}
}

type matcher func(object *tenancyv1alpha1.Workspace) error

func expectNextEvent(w watch.Interface, expectType watch.EventType, matcher matcher, duration time.Duration) (*tenancyv1alpha1.Workspace, error) {
	event, err := nextEvent(w, duration)
	if err != nil {
		return nil, err
	}
	if expectType != event.Type {
		return nil, fmt.Errorf("got incorrect watch event type: %v != %v\n", expectType, event.Type)
	}
	workspace, ok := event.Object.(*tenancyv1alpha1.Workspace)
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
