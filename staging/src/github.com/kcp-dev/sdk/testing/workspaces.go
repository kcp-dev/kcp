/*
Copyright 2022 The KCP Authors.

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

package testing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

const (
	// workspaceInitTimeout is set to 180 seconds. wait.ForeverTestTimeout
	// is 30 seconds and the optimism on that being forever is great, but workspace
	// initialisation can take a while in CI.
	workspaceInitTimeout = 120 * time.Second
)

// WorkspaceOption is an option for creating a workspace.
type WorkspaceOption interface {
	PrivilegedWorkspaceOption | UnprivilegedWorkspaceOption
}

// PrivilegedWorkspaceOption is an option that requires system:master permissions.
type PrivilegedWorkspaceOption func(ws *tenancyv1alpha1.Workspace)

// UnprivilegedWorkspaceOption is an option that does not require system:master permissions.
type UnprivilegedWorkspaceOption func(ws *tenancyv1alpha1.Workspace)

// WithRootShard schedules the workspace on the root shard.
func WithRootShard() UnprivilegedWorkspaceOption {
	return WithShard(corev1alpha1.RootShard)
}

// WithShard schedules the workspace on the given shard.
func WithShard(name string) UnprivilegedWorkspaceOption {
	return WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"name": name,
		},
	}})
}

// WithLocation sets the location of the workspace.
func WithLocation(w tenancyv1alpha1.WorkspaceLocation) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Spec.Location = &w
	}
}

// WithType sets the type of the workspace.
func WithType(path logicalcluster.Path, name tenancyv1alpha1.WorkspaceTypeName) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Spec.Type = &tenancyv1alpha1.WorkspaceTypeReference{
			Name: name,
			Path: path.String(),
		}
	}
}

// WithName sets the name of the workspace.
func WithName(s string, formatArgs ...interface{}) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Name = fmt.Sprintf(s, formatArgs...)
		ws.GenerateName = ""
	}
}

// WithNamePrefix make the workspace be named with the given prefix plus "-".
func WithNamePrefix(prefix string) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.GenerateName += prefix + "-"
	}
}

// NewLowLevelWorkspaceFixture creates a new workspace under the given parent
// using the given client. Don't use this if NewWorkspaceFixture can be used.
func NewLowLevelWorkspaceFixture[O WorkspaceOption](t TestingT, createClusterClient, clusterClient kcpclientset.ClusterInterface, parent logicalcluster.Path, options ...O) *tenancyv1alpha1.Workspace {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	tmpl := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName("universal"),
				Path: "root",
			},
		},
	}
	for _, opt := range options {
		opt(tmpl)
	}

	// we are referring here to a WorkspaceType that may have just been created; if the admission controller
	// does not have a fresh enough cache, our request will be denied as the admission controller does not know the
	// type exists. Therefore, we can require.Eventually our way out of this problem. We expect users to create new
	// types very infrequently, so we do not think this will be a serious UX issue in the product.
	var ws *tenancyv1alpha1.Workspace
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		ws, err = createClusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Create(ctx, tmpl, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating workspace under %s: %v", parent, err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create %s workspace under %s", tmpl.Spec.Type.Name, parent)

	wsName := ws.Name
	t.Cleanup(func() {
		if os.Getenv("PRESERVE") != "" {
			return
		}

		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			err := clusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Delete(ctx, wsName, metav1.DeleteOptions{})
			// ignore not found and forbidden because this probably means the parent has been deleted
			if err == nil || apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
				return true, ""
			}
			return false, err.Error()
		}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to delete workspace %s", wsName)
	})

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		ws, err = clusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", parent.Join(ws.Name))
		require.NoError(t, err, "failed to get workspace %s", parent.Join(ws.Name))
		if actual, expected := ws.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
			return false, fmt.Sprintf("workspace phase is %s, not %s", actual, expected)
		}
		return true, ""
	}, workspaceInitTimeout, time.Millisecond*100, "failed to wait for %s workspace %s to become ready", ws.Spec.Type, parent.Join(ws.Name))

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		if _, err := clusterClient.Cluster(logicalcluster.NewPath(ws.Spec.Cluster)).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{}); err != nil {
			return false, fmt.Sprintf("failed to get LogicalCluster %s by cluster name %s: %v", parent.Join(ws.Name), ws.Spec.Cluster, err)
		}
		if _, err := clusterClient.Cluster(parent.Join(ws.Name)).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{}); err != nil {
			return false, fmt.Sprintf("failed to get LogicalCluster %s via path: %v", parent.Join(ws.Name), err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for %s workspace %s to become accessible", ws.Spec.Type, parent.Join(ws.Name))

	t.Logf("Created %s workspace %s as /clusters/%s on shard %q", ws.Spec.Type, parent.Join(ws.Name), ws.Spec.Cluster, WorkspaceShardOrDie(t, clusterClient, ws).Name)
	return ws
}

// NewWorkspaceFixture creates a new workspace under the given parent.
func NewWorkspaceFixture(t TestingT, server kcptestingserver.RunningServer, parent logicalcluster.Path, options ...UnprivilegedWorkspaceOption) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	ws := NewLowLevelWorkspaceFixture(t, clusterClient, clusterClient, parent, options...)
	return parent.Join(ws.Name), ws
}

// WorkspaceShard returns the shard that a workspace is scheduled on.
func WorkspaceShard(ctx context.Context, kcpClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace) (*corev1alpha1.Shard, error) {
	shards, err := kcpClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// best effort to get a shard name from the hash in the annotation
	hash := ws.Annotations["internal.tenancy.kcp.io/shard"]
	if hash == "" {
		return nil, fmt.Errorf("workspace %s does not have a shard hash annotation", logicalcluster.From(ws).Path().Join(ws.Name))
	}

	for i := range shards.Items {
		if name := shards.Items[i].Name; base36Sha224NameValue(name) == hash {
			return &shards.Items[i], nil
		}
	}

	return nil, fmt.Errorf("failed to determine shard for workspace %s", ws.Name)
}

// WorkspaceShardOrDie returns the shard that a workspace is scheduled on, or
// fails the test on error.
func WorkspaceShardOrDie(t TestingT, kcpClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace) *corev1alpha1.Shard {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	shard, err := WorkspaceShard(ctx, kcpClient, ws)
	require.NoError(t, err, "failed to determine shard for workspace %s", ws.Name)
	return shard
}

func base36Sha224NameValue(name string) string {
	hash := sha256.Sum224([]byte(name))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))

	return base36hash[:8]
}
