/*
Copyright 2026 The KCP Authors.

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

package cache

import (
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	cache2e "github.com/kcp-dev/kcp/test/e2e/reconciler/cache"
)

func TestCacheServerAuthz(t *testing.T) {
	t.Skip("Skipped due to upstream data race in k8s.io/component-base/metrics.(*Histogram).WithContext() during concurrent x509 authentication")
	t.Parallel()
	framework.Suite(t, "control-plane")

	_, dataDir, err := kcptestingserver.ScratchDirs(t)
	require.NoError(t, err)

	cacheKubeconfigPath := cache2e.StartStandaloneCacheServer(t.Context(), t, dataDir)
	cacheServerKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
	require.NoError(t, err)

	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(*cacheServerKubeConfig, "cache", nil, nil)
	adminRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)

	t.Run("HealthEndpointsWithoutAuth", func(t *testing.T) {
		t.Parallel()
		testHealthEndpointsWithoutAuth(t, adminRestConfig)
	})
	t.Run("AnonymousAccessToCacheAPIIsDenied", func(t *testing.T) {
		t.Parallel()
		testAnonymousAccessDenied(t, adminRestConfig)
	})
	t.Run("CertSignedByDifferentCAIsRejected", func(t *testing.T) {
		t.Parallel()
		testUntrustedCertRejected(t, adminRestConfig, dataDir)
	})
	t.Run("ValidCertWithoutSystemMastersIsForbidden", func(t *testing.T) {
		t.Parallel()
		testUnprivilegedCertForbidden(t, adminRestConfig, dataDir)
	})
	t.Run("ValidCertWithSystemMastersIsAllowed", func(t *testing.T) {
		t.Parallel()
		testPrivilegedCertAllowed(t, adminRestConfig)
	})
}

// testHealthEndpointsWithoutAuth verifies that health endpoints are accessible without any client certificate.
func testHealthEndpointsWithoutAuth(t *testing.T, adminCfg *rest.Config) {
	t.Helper()

	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	client, err := rest.UnversionedRESTClientFor(cfg)
	require.NoError(t, err)

	for _, endpoint := range []string{"/healthz", "/readyz", "/livez"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			body, err := rest.NewRequest(client).RequestURI(endpoint).Do(t.Context()).Raw()
			require.NoError(t, err, "expected %s to be accessible without auth, got error", endpoint)
			t.Logf("%s returned: %s", endpoint, string(body))
		})
	}
}

// testAnonymousAccessDenied verifies that anonymous requests to API resources are forbidden.
func testAnonymousAccessDenied(t *testing.T, adminCfg *rest.Config) {
	t.Helper()

	cfg := cache2e.ClientRoundTrippersFor(rest.AnonymousClientConfig(adminCfg))
	dynamicClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports"}
	_, err = dynamicClient.Cluster(logicalcluster.NewPath("acme")).Resource(gvr).List(
		cacheclient.WithShardInContext(t.Context(), shard.New("amber")),
		metav1.ListOptions{},
	)
	require.Error(t, err)
	require.Truef(t, apierrors.IsForbidden(err), "expected Forbidden error, got: %v", err)
}

// testUntrustedCertRejected verifies that a cert signed by an unknown CA does not grant access.
func testUntrustedCertRejected(t *testing.T, adminCfg *rest.Config, dataDir string) {
	t.Helper()

	untrustedCADir := path.Join(dataDir, "untrusted-ca")
	untrustedCA, _, err := crypto.EnsureCA(
		path.Join(untrustedCADir, "ca.crt"),
		path.Join(untrustedCADir, "ca.key"),
		path.Join(untrustedCADir, "ca-serial.txt"),
		"untrusted-ca", 365,
	)
	require.NoError(t, err)

	_, _, err = untrustedCA.EnsureClientCertificate(
		path.Join(untrustedCADir, "client.crt"),
		path.Join(untrustedCADir, "client.key"),
		&user.DefaultInfo{Name: "untrusted-client", Groups: []string{"system:masters"}},
		365,
	)
	require.NoError(t, err)

	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.CertFile = path.Join(untrustedCADir, "client.crt")
	cfg.KeyFile = path.Join(untrustedCADir, "client.key")
	cfg = cache2e.ClientRoundTrippersFor(cfg)
	dynamicClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports"}
	_, err = dynamicClient.Cluster(logicalcluster.NewPath("acme")).Resource(gvr).List(
		cacheclient.WithShardInContext(t.Context(), shard.New("amber")),
		metav1.ListOptions{},
	)
	require.Error(t, err)
	isForbidden := apierrors.IsForbidden(err)
	require.Truef(t, isForbidden, "expected TLS-related error or Forbidden, got: %v", err)
}

// testUnprivilegedCertForbidden verifies that a valid cert without system:masters is forbidden from API access.
func testUnprivilegedCertForbidden(t *testing.T, adminCfg *rest.Config, dataDir string) {
	t.Helper()

	cacheDir := path.Join(dataDir, "cache")
	serverClientCA, err := crypto.GetCA(
		path.Join(cacheDir, "client-ca.crt"),
		path.Join(cacheDir, "client-ca.key"),
		path.Join(cacheDir, "client-ca-serial.txt"),
	)
	require.NoError(t, err)

	_, _, err = serverClientCA.EnsureClientCertificate(
		path.Join(cacheDir, "unprivileged-client.crt"),
		path.Join(cacheDir, "unprivileged-client.key"),
		&user.DefaultInfo{Name: "authz-test-user", Groups: []string{"some-other-group"}},
		365,
	)
	require.NoError(t, err)

	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.CertFile = path.Join(cacheDir, "unprivileged-client.crt")
	cfg.KeyFile = path.Join(cacheDir, "unprivileged-client.key")
	cfg = cache2e.ClientRoundTrippersFor(cfg)
	dynamicClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports"}
	_, err = dynamicClient.Cluster(logicalcluster.NewPath("acme")).Resource(gvr).List(
		cacheclient.WithShardInContext(t.Context(), shard.New("amber")),
		metav1.ListOptions{},
	)
	require.Error(t, err)
	require.Truef(t, apierrors.IsForbidden(err), "expected Forbidden error, got: %v", err)
}

// testPrivilegedCertAllowed verifies that a valid cert with system:masters can access the API.
func testPrivilegedCertAllowed(t *testing.T, adminCfg *rest.Config) {
	t.Helper()

	cfg := cache2e.ClientRoundTrippersFor(adminCfg)
	dynamicClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	gvr := schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexports"}
	_, err = dynamicClient.Cluster(logicalcluster.NewPath("acme")).Resource(gvr).List(
		cacheclient.WithShardInContext(t.Context(), shard.New("amber")),
		metav1.ListOptions{},
	)
	require.NoError(t, err, "expected system:masters cert to be allowed")
}
