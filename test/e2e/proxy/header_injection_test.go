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
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestIdentityHeaderInjectionDoesNotEscalate is the test
// for the front-proxy / local-proxy identity-header injection.
//
// The shard trusts X-Remote-User / X-Remote-Group / X-Remote-Extra-* verbatim
// when they arrive over the front-proxy's mutually-authenticated connection. An
// attacker that authenticates by any non-request-header method (here: a bearer
// token) and injects X-Remote-Group: system:masters (or a forged
// authorization.kcp.io/warrant) must NOT have those values honored:
//   - via the front-proxy, because the proxy strips inbound copies before
//     stamping the real identity;
//   - directly to a shard, because the shard's request-header authenticator only
//     trusts these headers over a connection bearing a client cert signed by the
//     request-header CA, which the attacker does not possess.
func TestIdentityHeaderInjectionDoesNotEscalate(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	// The headers an attacker would inject. None of them must grant privilege.
	// Note: X-Remote-User is deliberately omitted — omitting it is what avoided
	// the upstream request-header authenticator's conditional header clearing in
	// the original vulnerability.
	forgedWarrant := `{"user":"admin","groups":["system:masters"]}`
	forged := map[string][]string{
		"X-Remote-Group": {"system:masters"},
		"X-Remote-Extra-Authorization.kcp.io%2fwarrant": {forgedWarrant},
		"X-Remote-Extra-Authentication.kcp.io%2fscopes": {"cluster:root"},
	}

	// probe lists secrets in the root logical cluster. This is forbidden for a
	// low-privilege user but allowed for system:masters, so if any forged header
	// survived to the shard authorizer the call would succeed instead of 403.
	probe := func(t *testing.T, cfg *rest.Config) error {
		t.Helper()
		client, err := kcpkubernetesclientset.NewForConfig(cfg)
		require.NoError(t, err)
		_, err = client.Cluster(logicalcluster.NewPath("root")).CoreV1().Secrets("default").List(t.Context(), metav1.ListOptions{})
		return err
	}

	t.Run("via front-proxy", func(t *testing.T) {
		t.Parallel()
		base := framework.StaticTokenUserConfig("user-1", server.BaseConfig(t))

		// Precondition: without injection, user-1 is forbidden in root.
		require.Truef(t, apierrors.IsForbidden(probe(t, base)),
			"precondition: low-privilege user-1 must be forbidden listing secrets in root")

		// With injection: must STILL be forbidden — the front-proxy strips the
		// forged headers before stamping user-1's real identity.
		err := probe(t, withInjectedHeaders(base, forged))
		require.Truef(t, apierrors.IsForbidden(err),
			"front-proxy must strip forged identity headers; escalation succeeded or unexpected error: %v", err)
	})

	t.Run("directly to shard", func(t *testing.T) {
		t.Parallel()
		// user-1 token over a plain (no client cert) connection straight to the
		// root shard, bypassing the front-proxy.
		base := framework.StaticTokenUserConfig("user-1", server.RootShardSystemMasterBaseConfig(t))

		// Precondition: without injection, user-1 is forbidden in root.
		require.Truef(t, apierrors.IsForbidden(probe(t, base)),
			"precondition: low-privilege user-1 must be forbidden listing secrets in root on the shard")

		// With injection: must STILL be forbidden — the shard's request-header
		// authenticator does not trust X-Remote-* over a connection that lacks a
		// client cert signed by the request-header CA.
		err := probe(t, withInjectedHeaders(base, forged))
		require.Truef(t, apierrors.IsForbidden(err),
			"shard must not honor request-header identity over a non-request-header connection; escalation succeeded or unexpected error: %v", err)
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
