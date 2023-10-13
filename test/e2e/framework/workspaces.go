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

package framework

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/admission/workspace"
	"github.com/kcp-dev/kcp/pkg/authorization"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	reconcilerworkspace "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspace"
	"github.com/kcp-dev/kcp/pkg/server"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

const (
	// workspaceInitTimeout is set to 60 seconds. wait.ForeverTestTimeout
	// is 30 seconds and the optimism on that being forever is great, but workspace
	// initialisation can take a while in CI.
	workspaceInitTimeout = 60 * time.Second
)

type WorkspaceOption interface {
	PrivilegedWorkspaceOption | UnprivilegedWorkspaceOption
}

type PrivilegedWorkspaceOption func(ws *tenancyv1alpha1.Workspace)

type UnprivilegedWorkspaceOption func(ws *tenancyv1alpha1.Workspace)

func WithRootShard() UnprivilegedWorkspaceOption {
	return WithShard(corev1alpha1.RootShard)
}

func TODO_WithoutMultiShardSupport() UnprivilegedWorkspaceOption {
	return WithRootShard()
}

func WithShard(name string) UnprivilegedWorkspaceOption {
	return WithLocation(tenancyv1alpha1.WorkspaceLocation{Selector: &metav1.LabelSelector{
		MatchLabels: map[string]string{
			"name": name,
		},
	}})
}

func WithLocation(w tenancyv1alpha1.WorkspaceLocation) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Spec.Location = &w
	}
}

// WithRequiredGroups is a privileged action, so we return a privileged option type, and only the helpers that
// use the system:master config can consume this. However, workspace initialization requires a valid user annotation
// on the workspace object to impersonate during initialization, and system:master bypasses setting that, so we
// end up needing to hard-code something conceivable.
func WithRequiredGroups(groups ...string) PrivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations[authorization.RequiredGroupsAnnotationKey] = strings.Join(groups, ",")
		userInfo, err := workspace.WorkspaceOwnerAnnotationValue(&user.DefaultInfo{
			Name:   server.KcpBootstrapperUserName,
			Groups: []string{user.AllAuthenticated, bootstrappolicy.SystemKcpWorkspaceBootstrapper},
		})
		if err != nil {
			// should never happen
			panic(err)
		}
		ws.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = userInfo
	}
}

func WithType(path logicalcluster.Path, name tenancyv1alpha1.WorkspaceTypeName) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Spec.Type = tenancyv1alpha1.WorkspaceTypeReference{
			Name: name,
			Path: path.String(),
		}
	}
}

func WithName(s string, formatArgs ...interface{}) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.Name = fmt.Sprintf(s, formatArgs...)
		ws.GenerateName = ""
	}
}

func WithNameSuffix(suffix string) UnprivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		ws.GenerateName += suffix + "-"
	}
}

func newWorkspaceFixture[O WorkspaceOption](t *testing.T, createClusterClient, clusterClient kcpclientset.ClusterInterface, parent logicalcluster.Path, options ...O) *tenancyv1alpha1.Workspace {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	tmpl := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: tenancyv1alpha1.WorkspaceTypeReference{
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
	Eventually(t, func() (bool, string) {
		var err error
		ws, err = createClusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Create(ctx, tmpl, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating workspace under %s: %v", parent, err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create %s workspace under %s", tmpl.Spec.Type.Name, parent)

	wsName := ws.Name
	t.Cleanup(func() {
		if preserveTestResources() {
			return
		}

		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		err := clusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Delete(ctx, wsName, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return // ignore not found and forbidden because this probably means the parent has been deleted
		}
		require.NoErrorf(t, err, "failed to delete workspace %s", wsName)
	})

	Eventually(t, func() (bool, string) {
		var err error
		ws, err = clusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", parent.Join(ws.Name))
		require.NoError(t, err, "failed to get workspace %s", parent.Join(ws.Name))
		if actual, expected := ws.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
			return false, fmt.Sprintf("workspace phase is %s, not %s", actual, expected)
		}
		return true, ""
	}, workspaceInitTimeout, time.Millisecond*100, "failed to wait for %s workspace %s to become ready", ws.Spec.Type, parent.Join(ws.Name))

	Eventually(t, func() (bool, string) {
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

func NewWorkspaceFixture(t *testing.T, server RunningServer, parent logicalcluster.Path, options ...UnprivilegedWorkspaceOption) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	ws := newWorkspaceFixture(t, clusterClient, clusterClient, parent, options...)
	return parent.Join(ws.Name), ws
}

func NewOrganizationFixture(t *testing.T, server RunningServer, options ...UnprivilegedWorkspaceOption) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()
	return NewWorkspaceFixture(t, server, core.RootCluster.Path(), append(options, WithType(core.RootCluster.Path(), "organization"))...)
}

func NewPrivilegedOrganizationFixture[O WorkspaceOption](t *testing.T, server RunningServer, options ...O) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()

	rootConfig := server.RootShardSystemMasterBaseConfig(t)
	rootClusterClient, err := kcpclientset.NewForConfig(rootConfig)
	require.NoError(t, err, "failed to construct client for server")

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	ws := newWorkspaceFixture(t, rootClusterClient, clusterClient, core.RootCluster.Path(), append(options, O(WithType(core.RootCluster.Path(), "organization")))...)
	return core.RootCluster.Path().Join(ws.Name), ws
}

func WorkspaceShard(ctx context.Context, kcpClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace) (*corev1alpha1.Shard, error) {
	shards, err := kcpClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// best effort to get a shard name from the hash in the annotation
	hash := ws.Annotations[reconcilerworkspace.WorkspaceShardHashAnnotationKey]
	if hash == "" {
		return nil, fmt.Errorf("workspace %s does not have a shard hash annotation", logicalcluster.From(ws).Path().Join(ws.Name))
	}

	for i := range shards.Items {
		if name := shards.Items[i].Name; reconcilerworkspace.ByBase36Sha224NameValue(name) == hash {
			return &shards.Items[i], nil
		}
	}

	return nil, fmt.Errorf("failed to determine shard for workspace %s", ws.Name)
}

func WorkspaceShardOrDie(t *testing.T, kcpClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace) *corev1alpha1.Shard {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	shard, err := WorkspaceShard(ctx, kcpClient, ws)
	require.NoError(t, err, "failed to determine shard for workspace %s", ws.Name)
	return shard
}

func VirtualWorkspaceURL(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace, urls []string) (string, bool, error) {
	shard, err := WorkspaceShard(ctx, kcpClusterClient, ws)
	if err != nil {
		return "", false, err
	}

	for _, url := range urls {
		if strings.HasPrefix(url, shard.Spec.VirtualWorkspaceURL) {
			return url, true, nil
		}
	}

	return "", false, nil
}

func VirtualWorkspaceURLOrDie(t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace, urls []string) string {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	url, found, err := VirtualWorkspaceURL(ctx, kcpClusterClient, ws, urls)
	require.NoError(t, err)
	require.True(t, found, "failed to find virtual workspace URL for workspace %s", ws.Name)
	return url
}

func ExportVirtualWorkspaceURLs(export *apisv1alpha1.APIExport) []string {
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	urls := make([]string, 0, len(export.Status.VirtualWorkspaces))
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	for _, vw := range export.Status.VirtualWorkspaces {
		urls = append(urls, vw.URL)
	}
	return urls
}
