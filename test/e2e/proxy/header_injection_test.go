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

package proxy

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestIdentityHeaderInjectionDoesNotEscalate is the end-to-end regression test
// for the front-proxy / local-proxy identity-header injection vulnerability: a
// low-privilege but authenticated client must not be able to escalate by
// smuggling its own X-Remote-* identity headers, neither when talking to a
// shard through the front-proxy nor when talking to a shard directly.
//
// The shard trusts X-Remote-User / X-Remote-Group / X-Remote-Extra-* verbatim
// when they arrive over the front-proxy's mutually-authenticated connection. An
// attacker that authenticates by any non-request-header method (here: a bearer
// token) and injects X-Remote-Group: system:masters (or a forged
// authorization.kcp.io/warrant) must NOT have those values honored:
//   - via the front-proxy, because the proxy strips inbound copies before
//     stamping the real identity (asserted directly by observing the headers the
//     front-proxy forwards to an echo backend);
//   - directly to a shard, because the shard's request-header authenticator only
//     trusts these headers over a connection bearing a client cert signed by the
//     request-header CA, which the attacker does not possess (asserted
//     behaviourally: a system:masters injection must not grant access).
//
// NOTE: this test is intentionally NOT parallel. It binds an echo backend on the
// fixed front-proxy mapping port (localhost:2443), which is shared with
// TestMappingWithClusterContext; running serially avoids a port clash.
//
//nolint:paralleltest // binds the fixed front-proxy mapping port localhost:2443, shared with TestMappingWithClusterContext; must run serially.
func TestIdentityHeaderInjectionDoesNotEscalate(t *testing.T) {
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	// This test only exercises a real attack surface when there is a front-proxy
	// stamping X-Remote-* onto requests forwarded to shards that trust those
	// headers (i.e. the sharded-test-server topology). A standalone `kcp start`
	// has neither a front-proxy nor request-header trust, so injected headers are
	// ignored by everything and the test would pass vacuously — which would hide
	// a regression. Skip rather than give a false green.
	//
	// Run against the sharded topology, e.g.:
	//   make test-e2e-sharded-minimal WHAT=./test/e2e/proxy/ \
	//     TEST_ARGS="-run TestIdentityHeaderInjectionDoesNotEscalate -v"
	if len(server.ShardNames()) < 2 {
		t.Skip("This test requires a multi-shard setup with a front-proxy.")
	}

	// The headers an attacker would inject. None of them must reach the shard as
	// the attacker controls them. X-Remote-User is included to prove it is
	// overwritten with the real (proxy-authenticated) identity rather than
	// honored; the group/extra headers are the actual escalation vectors.
	forgedWarrant := `{"user":"admin","groups":["system:masters"]}`
	forged := map[string][]string{
		"X-Remote-User":  {"admin"},
		"X-Remote-Group": {"system:masters"},
		"X-Remote-Extra-Authorization.kcp.io%2fwarrant": {forgedWarrant},
		"X-Remote-Extra-Authentication.kcp.io%2fscopes": {"cluster:root"},
	}

	//nolint:paralleltest // parent must run serially (shared fixed port); subtests inherit that.
	t.Run("via front-proxy strips injected identity headers", func(t *testing.T) {
		// Stand up an echo backend on the front-proxy's "/e2e" mapping target so
		// we can observe exactly which identity headers the front-proxy forwards.
		var mu sync.Mutex
		var lastHeaders http.Header
		echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			lastHeaders = r.Header.Clone()
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		})

		dirPath := filepath.Dir(server.KubeconfigPath())
		srv := &http.Server{
			Handler: echo,
			Addr:    "localhost:2443", // as per front-proxy mapping
			BaseContext: func(net.Listener) context.Context {
				return t.Context()
			},
		}
		go func() {
			if err := srv.ListenAndServeTLS(
				filepath.Join(dirPath, "apiserver.crt"),
				filepath.Join(dirPath, "apiserver.key"),
			); err != nil && err != http.ErrServerClosed {
				t.Logf("echo server stopped: %v", err)
			}
		}()
		t.Cleanup(func() { _ = srv.Close() })

		// A legitimate low-privilege tenant authenticated to the front-proxy by a
		// client certificate (NOT request-header auth), who injects forged
		// identity headers. The client cert is what makes the front-proxy
		// authenticate the request and stamp X-Remote-User — which is the
		// precondition that activates the shard's request-header path the attack
		// abuses.
		cfg := server.ClientCAUserConfig(t, server.BaseConfig(t), "attacker", "team-1")
		cfg.Host += "/e2e"
		client, err := kcpkubernetesclientset.NewForConfig(withInjectedHeaders(cfg, forged))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			_, _ = client.Cluster(logicalcluster.NewPath("root")).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
			mu.Lock()
			defer mu.Unlock()
			return lastHeaders != nil
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "echo backend never received a forwarded request")

		mu.Lock()
		defer mu.Unlock()

		// The front-proxy must stamp the real (cert-authenticated) identity,
		// overwriting the injected X-Remote-User...
		require.Equal(t, "attacker", lastHeaders.Get("X-Remote-User"),
			"front-proxy must stamp the authenticated user and overwrite the injected one")
		// ...and must NOT forward any client-supplied identity headers.
		require.NotContains(t, lastHeaders.Values("X-Remote-Group"), "system:masters",
			"front-proxy leaked a forged X-Remote-Group to the shard; got %v", lastHeaders.Values("X-Remote-Group"))
		require.Empty(t, lastHeaders.Values("X-Remote-Extra-Authorization.kcp.io%2fwarrant"),
			"front-proxy leaked a forged warrant extra to the shard")
		require.Empty(t, lastHeaders.Values("X-Remote-Extra-Authentication.kcp.io%2fscopes"),
			"front-proxy leaked a forged scopes extra to the shard")
	})

	//nolint:paralleltest // parent must run serially (shared fixed port); subtests inherit that.
	t.Run("directly to shard ignores injected identity headers", func(t *testing.T) {
		// user-1 token over a plain (no client cert) connection straight to the
		// root shard, bypassing the front-proxy. Listing secrets in root is
		// forbidden for user-1 but allowed for system:masters, so a successful
		// list would prove the shard honored the injected header.
		base := framework.StaticTokenUserConfig("user-1", server.RootShardSystemMasterBaseConfig(t))

		probe := func(cfg *rest.Config) error {
			client, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err)
			_, err = client.Cluster(logicalcluster.NewPath("root")).CoreV1().Secrets("default").List(t.Context(), metav1.ListOptions{})
			return err
		}

		// Precondition: without injection, user-1 is forbidden in root.
		require.Truef(t, apierrors.IsForbidden(probe(base)),
			"precondition: low-privilege user-1 must be forbidden listing secrets in root on the shard")

		// With injection: must STILL be forbidden — the shard's request-header
		// authenticator does not trust X-Remote-* over a connection that lacks a
		// client cert signed by the request-header CA.
		err := probe(withInjectedHeaders(base, forged))
		require.Truef(t, apierrors.IsForbidden(err),
			"shard honored injected request-header identity over a non-request-header connection; escalation succeeded or unexpected error: %v", err)
	})
}

// withInjectedHeaders returns a copy of cfg whose transport adds the given
// headers to every request, simulating a client that injects its own X-Remote-*
// identity headers on top of its legitimate authentication.
func withInjectedHeaders(cfg *rest.Config, headers map[string][]string) *rest.Config {
	cfg = rest.CopyConfig(cfg)
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			for k, vs := range headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			return rt.RoundTrip(req)
		})
	})
	return cfg
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
