/*
Copyright 2026 The kcp Authors.

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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestIndexWorkspaceByLogicalCluster(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  *tenancyv1alpha1.Workspace
		want []string
	}{
		{
			name: "scheduled workspace indexed by spec.cluster",
			obj: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{Cluster: "root-foo-bar"},
			},
			want: []string{"root-foo-bar"},
		},
		{
			name: "unscheduled workspace yields empty index",
			obj: &tenancyv1alpha1.Workspace{
				Spec: tenancyv1alpha1.WorkspaceSpec{Cluster: ""},
			},
			want: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := indexWorkspaceByLogicalCluster(tc.obj)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEnqueueLogicalCluster(t *testing.T) {
	const (
		parentCluster  = "root:foo"
		childCluster   = "root-foo-bar"
		workspaceName  = "bar"
		otherWorkspace = "baz"
	)

	workspaceIndexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		byLogicalCluster: indexWorkspaceByLogicalCluster,
	})

	// Workspace on shard whose spec.cluster matches the LogicalCluster.
	matching := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        workspaceName,
			Annotations: map[string]string{"kcp.io/cluster": parentCluster},
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{Cluster: childCluster},
	}
	// Unrelated workspace that must NOT be enqueued.
	unrelated := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        otherWorkspace,
			Annotations: map[string]string{"kcp.io/cluster": parentCluster},
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{Cluster: "some-other-cluster"},
	}
	require.NoError(t, workspaceIndexer.Add(matching))
	require.NoError(t, workspaceIndexer.Add(unrelated))

	for _, tc := range []struct {
		name      string
		lc        *corev1alpha1.LogicalCluster
		wantItems []string
	}{
		{
			name: "LogicalCluster event enqueues matching workspace only",
			lc: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        corev1alpha1.LogicalClusterName,
					Annotations: map[string]string{"kcp.io/cluster": childCluster},
				},
			},
			wantItems: []string{"root:foo|bar"},
		},
		{
			name: "LogicalCluster with no matching workspace enqueues nothing",
			lc: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        corev1alpha1.LogicalClusterName,
					Annotations: map[string]string{"kcp.io/cluster": "orphan-cluster"},
				},
			},
			wantItems: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := workqueue.NewTypedRateLimitingQueueWithConfig(
				workqueue.DefaultTypedControllerRateLimiter[string](),
				workqueue.TypedRateLimitingQueueConfig[string]{Name: "test"},
			)
			c := &Controller{
				queue:            queue,
				workspaceIndexer: workspaceIndexer,
			}

			c.enqueueLogicalCluster(tc.lc)

			got := drainQueue(queue)
			require.ElementsMatch(t, tc.wantItems, got)
		})
	}
}

func drainQueue(q workqueue.TypedRateLimitingInterface[string]) []string {
	// Give AddAfter/Add a brief moment to land; Add is synchronous, so this
	// is belt-and-suspenders for any future indirection.
	deadline := time.Now().Add(100 * time.Millisecond)
	var items []string
	for time.Now().Before(deadline) && q.Len() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	for q.Len() > 0 {
		item, shutdown := q.Get()
		if shutdown {
			break
		}
		items = append(items, item)
		q.Done(item)
	}
	return items
}
