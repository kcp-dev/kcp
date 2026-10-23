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

package admin

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAdminWorkspaceAPIIdentities exercises the identity registry view of the
// Admin workspace (/services/admin): the core identities are listed with
// their root exports as owners, a new export's identity shows up with the
// export as owner and is delivered on a watch, bookkeeping annotations are
// stripped, and the view is read-only.
func TestAdminWorkspaceAPIIdentities(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err)

	vwCfg := rest.CopyConfig(cfg)
	vwURL, err := url.Parse(cfg.Host)
	require.NoError(t, err)
	vwURL.Path = "/services/admin"
	vwCfg.Host = vwURL.String()
	adminClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Logf("The core identities are registered with their root exports as owners")
	tenancyExport, err := kcpClusterClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, "tenancy.kcp.io", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, tenancyExport.Status.IdentityHash)

	var registry *adminv1alpha1.APIIdentityList
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		registry, err = adminClient.AdminV1alpha1().APIIdentities().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		var tenancy *adminv1alpha1.APIIdentity
		for i := range registry.Items {
			require.NotContains(c, registry.Items[i].Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
			if registry.Items[i].Name == tenancyExport.Status.IdentityHash {
				tenancy = &registry.Items[i]
			}
		}
		require.NotNil(c, tenancy, "the tenancy.kcp.io identity must be registered")
		require.True(c, tenancy.Status.Core, "tenancy.kcp.io is a core identity")
		require.Equal(c, adminv1alpha1.ExportReference{Cluster: core.RootCluster.String(), Name: "tenancy.kcp.io", UID: string(tenancyExport.UID)}, tenancy.Status.Owner)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Get the core identity by name through the Admin workspace")
	for _, rv := range []string{"", "0", registry.ResourceVersion} {
		entry, err := adminClient.AdminV1alpha1().APIIdentities().Get(ctx, tenancyExport.Status.IdentityHash, metav1.GetOptions{ResourceVersion: rv})
		require.NoError(t, err, "failed to get identity with resourceVersion %q", rv)
		require.Equal(t, tenancyExport.Status.IdentityHash, entry.Name)
		require.NotContains(t, entry.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
	}
	_, err = adminClient.AdminV1alpha1().APIIdentities().Get(ctx, "does-not-exist", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)

	t.Logf("A new APIExport's identity is registered with the export as owner, and delivered on a watch")
	w, err := adminClient.AdminV1alpha1().APIIdentities().Watch(ctx, metav1.ListOptions{ResourceVersion: registry.ResourceVersion})
	require.NoError(t, err)
	t.Cleanup(w.Stop)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		export, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, export.Name, metav1.GetOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, export.Status.IdentityHash, "the export must get an identity")
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	want := adminv1alpha1.ExportReference{Cluster: logicalcluster.Name(providerWS.Spec.Cluster).String(), Name: export.Name, UID: string(export.UID)}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		entry, err := adminClient.AdminV1alpha1().APIIdentities().Get(ctx, export.Status.IdentityHash, metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, want, entry.Status.Owner)
		require.False(c, entry.Status.Core)
		require.NotContains(c, entry.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	deadline := time.After(wait.ForeverTestTimeout)
	for seen := false; !seen; {
		select {
		case event, ok := <-w.ResultChan():
			require.True(t, ok, "the watch closed before the new identity was delivered")
			if event.Type == watch.Bookmark || event.Type == watch.Error {
				continue
			}
			entry, ok := event.Object.(*adminv1alpha1.APIIdentity)
			require.True(t, ok, "unexpected watch object %T", event.Object)
			require.NotContains(t, entry.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
			seen = entry.Name == export.Status.IdentityHash && entry.Status.Owner.Equal(want)
		case <-deadline:
			t.Fatalf("timed out waiting for the watch to deliver identity %s", export.Status.IdentityHash)
		}
	}

	t.Logf("The registry is read-only through the Admin workspace")
	entry, err := adminClient.AdminV1alpha1().APIIdentities().Get(ctx, export.Status.IdentityHash, metav1.GetOptions{})
	require.NoError(t, err)
	entry.Status.Core = true
	_, err = adminClient.AdminV1alpha1().APIIdentities().Update(ctx, entry, metav1.UpdateOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsMethodNotSupported(err) || apierrors.IsForbidden(err), "expected the update to be rejected, got %v", err)
	err = adminClient.AdminV1alpha1().APIIdentities().Delete(ctx, entry.Name, metav1.DeleteOptions{})
	require.Error(t, err)
	require.True(t, apierrors.IsMethodNotSupported(err) || apierrors.IsForbidden(err), "expected the delete to be rejected, got %v", err)
}
