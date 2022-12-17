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

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
)

type ClusterWorkspaceOption func(ws *tenancyv1alpha1.ClusterWorkspace)

func WithShardConstraints(c tenancyv1alpha1.ShardConstraints) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.ClusterWorkspace) {
		ws.Spec.Shard = &c
	}
}

func WithRequiredGroups(groups ...string) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.ClusterWorkspace) {
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations[authorization.RequiredGroupsAnnotationKey] = strings.Join(groups, ",")
	}
}

func WithType(path logicalcluster.Path, name tenancyv1alpha1.WorkspaceTypeName) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.ClusterWorkspace) {
		ws.Spec.Type = tenancyv1alpha1.WorkspaceTypeReference{
			Name: name,
			Path: path.String(),
		}
	}
}

func WithName(s string, formatArgs ...interface{}) ClusterWorkspaceOption {
	return func(ws *tenancyv1alpha1.ClusterWorkspace) {
		ws.Name = fmt.Sprintf(s, formatArgs...)
		ws.GenerateName = ""
	}
}

func NewWorkspaceFixtureObject(t *testing.T, server RunningServer, orgClusterName logicalcluster.Path, options ...ClusterWorkspaceOption) *tenancyv1alpha1.ClusterWorkspace {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	tmpl := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
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
	var ws *tenancyv1alpha1.ClusterWorkspace
	require.Eventually(t, func() bool {
		var err error
		ws, err = clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, tmpl, metav1.CreateOptions{})
		if err != nil {
			t.Logf("error creating workspace under %s: %v", orgClusterName, err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace under %s", orgClusterName)

	t.Cleanup(func() {
		if preserveTestResources() {
			return
		}

		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return // ignore not found and forbidden because this probably means the parent has been deleted
		}
		require.NoErrorf(t, err, "failed to delete workspace %s", ws.Name)
	})

	Eventually(t, func() (bool, string) {
		ws, err = clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", ws.Name)
		require.NoError(t, err, "failed to get workspace %s", ws.Name)
		if actual, expected := ws.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
			return false, fmt.Sprintf("workspace phase is %s, not %s", actual, expected)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for workspace %s to become ready", orgClusterName.Join(ws.Name))

	t.Logf("Created %s workspace %s", ws.Spec.Type, orgClusterName.Join(ws.Name))
	return ws
}

func NewWorkspaceFixture(t *testing.T, server RunningServer, orgClusterName logicalcluster.Path, options ...ClusterWorkspaceOption) (clusterName logicalcluster.Name) {
	ws := NewWorkspaceFixtureObject(t, server, orgClusterName, options...)
	return logicalcluster.Name(ws.Status.Cluster)
}

func NewOrganizationFixtureObject(t *testing.T, server RunningServer, options ...ClusterWorkspaceOption) *tenancyv1alpha1.ClusterWorkspace {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to create kcp cluster client")

	tmpl := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-org-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: tenancyv1alpha1.WorkspaceTypeReference{
				Name: "organization",
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
	var org *tenancyv1alpha1.ClusterWorkspace
	require.Eventually(t, func() bool {
		var err error
		org, err = clusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, tmpl, metav1.CreateOptions{})
		if err != nil {
			t.Logf("error creating org workspace under %s: %v", tenancyv1alpha1.RootCluster, err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create org workspace under %s", tenancyv1alpha1.RootCluster)

	t.Cleanup(func() {
		if preserveTestResources() {
			return
		}

		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		err := clusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, org.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return // ignore not found error
		}
		require.NoErrorf(t, err, "failed to delete organization workspace %s", org.Name)
	})

	Eventually(t, func() (bool, string) {
		org, err = clusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", org.Name)
		require.NoError(t, err, "failed to get workspace %s", org.Name)
		if actual, expected := org.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
			return false, fmt.Sprintf("workspace phase is %s, not %s", actual, expected)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for organization workspace %s to become ready", org.Name)

	t.Logf("Created organization workspace %s", tenancyv1alpha1.RootCluster.Path().Join(org.Name))
	return org
}

func NewOrganizationFixture(t *testing.T, server RunningServer, options ...ClusterWorkspaceOption) (orgClusterName logicalcluster.Name) {
	org := NewOrganizationFixtureObject(t, server, options...)
	return logicalcluster.Name(org.Status.Cluster)
}
