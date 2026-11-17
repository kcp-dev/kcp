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

package logicalclustermigration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMigrationVerification(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	var proxy *httputil.ReverseProxy
	proxyReady := make(chan struct{})
	var tamperDigest atomic.Bool
	tamperDigest.Store(true)

	vwProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-proxyReady:
		case <-r.Context().Done():
			return
		}
		recorder := httptest.NewRecorder()
		proxy.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		body := recorder.Body.Bytes()
		if tamperDigest.Load() && recorder.Code == http.StatusCreated && strings.HasSuffix(r.URL.Path, migratingworkspaces.DigestHandlerPath) {
			var digest migrationv1alpha1.LogicalClusterDigest
			if err := json.Unmarshal(body, &digest); err == nil && len(digest.Status.Entries) > 0 {
				digest.Status.Entries[0].Hash = "corrupted-digest"
				if tampered, err := json.Marshal(&digest); err == nil {
					body = tampered
					w.Header().Del("Content-Length")
				}
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(body)
	}))
	t.Cleanup(vwProxy.Close)

	server := privateMigrationServer(t, "--shard-virtual-workspace-url="+vwProxy.URL)
	cfg := server.BaseConfig(t)
	client, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kube, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	org, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithShard("root"))
	path, ws := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithShard("root"))
	cm, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "verified-cm"},
		Data:       map[string]string{"value": "original"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	bindExportForWildcardWatchTest(t, client, org, "migration.kcp.io", core.RootCluster.Path(), nil)

	shards := client.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards()
	origin, err := shards.Get(t.Context(), "root", metav1.GetOptions{})
	require.NoError(t, err)
	target, err := url.Parse(origin.Spec.BaseURL)
	require.NoError(t, err)
	dumpConfig := rest.CopyConfig(cfg)
	dumpConfig.Impersonate = rest.ImpersonationConfig{UserName: "verification-test", Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}}
	transport, err := rest.TransportFor(dumpConfig)
	require.NoError(t, err)
	proxy = httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	close(proxyReady)

	migrations := client.Cluster(org).MigrationV1alpha1().LogicalClusterMigrations()
	_, err = migrations.Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "verify-mismatch"},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: ws.Spec.Cluster, DestinationShard: "shard-1"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		m, err := migrations.Get(t.Context(), "verify-mismatch", metav1.GetOptions{})
		if err != nil {
			return false
		}
		verified := conditions.Get(m, migrationv1alpha1.LCMigrationDataVerified)
		return m.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCancelled &&
			verified != nil && verified.Reason == "VerificationFailed" &&
			conditions.IsTrue(m, migrationv1alpha1.LCMigrationCancelled)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "digest mismatch must cancel migration and mark DataVerified=False")

	require.Eventually(t, func() bool {
		got, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		return err == nil && got.UID == cm.UID && got.Data["value"] == "original"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace must remain accessible with its original data after verification failure")

	for _, name := range []string{"root", "shard-1"} {
		shard, err := shards.Get(t.Context(), name, metav1.GetOptions{})
		require.NoError(t, err)
		local := rest.CopyConfig(cfg)
		local.Host = shard.Spec.BaseURL
		localClient, err := kcpkubernetesclientset.NewForConfig(local)
		require.NoError(t, err)
		got, err := localClient.Cluster(logicalcluster.NewPath(ws.Spec.Cluster)).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		if name == "root" {
			require.NoError(t, err)
			require.Equal(t, cm.UID, got.UID)
		} else {
			require.True(t, apierrors.IsNotFound(err), "destination shard must have cleaned up its unverified copy: %v", err)
		}
	}

	// Stop tampering with digests and verify a subsequent migration succeeds with DataVerified=True.
	tamperDigest.Store(false)
	_, err = migrations.Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "verify-success"},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: ws.Spec.Cluster, DestinationShard: "shard-1"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		m, err := migrations.Get(t.Context(), "verify-success", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return m.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCompleted &&
			conditions.IsTrue(m, migrationv1alpha1.LCMigrationDataCopied) &&
			conditions.IsTrue(m, migrationv1alpha1.LCMigrationDataVerified)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "valid migration must complete with DataVerified=True")

	require.Eventually(t, func() bool {
		got, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		return err == nil && got.UID == cm.UID && got.Data["value"] == "original"
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
