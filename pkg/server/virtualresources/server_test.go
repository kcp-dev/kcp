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

package virtualresources

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/proxy/authheaders"
)

// forward runs a request through the proxy's rewrite and returns the headers
// the virtual workspace would see.
func forward(t *testing.T, req *http.Request) http.Header {
	t.Helper()

	handler, err := newVirtualResourceHandler(&rest.Config{Host: "https://vw.example.com"},
		"https://vw.example.com/services/ephemeral/cluster/export", "cluster", 0)
	require.NoError(t, err)

	proxy, ok := handler.(*httputil.ReverseProxy)
	require.True(t, ok, "the handler is expected to be a reverse proxy")

	// The outbound request starts as a copy of the inbound one, as it does in
	// ReverseProxy.ServeHTTP, so anything the client sent is there to be stripped.
	outbound := req.Clone(req.Context())
	proxy.Rewrite(&httputil.ProxyRequest{In: req, Out: outbound})

	return outbound.Header
}

// The virtual workspace authorizes whoever these headers name, and asking a
// provider's webhook "who is this for" is the point of the feature. Without
// them the connection's own identity -- this shard -- is what arrives.
func TestForwardsTheCallersIdentity(t *testing.T) {
	t.Parallel()

	ctx := genericapirequest.WithUser(t.Context(), &user.DefaultInfo{
		Name:   "alice",
		Groups: []string{"team-a", "system:authenticated"},
		Extra:  map[string][]string{"authentication.kcp.io/scopes": {"cluster:root"}},
	})
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://shard.example.com/apis/s3.dev/v1alpha1/bucketinfos", http.NoBody)

	header := forward(t, req)

	require.Equal(t, "alice", header.Get(authheaders.DefaultUserHeader))
	require.Equal(t, []string{"team-a", "system:authenticated"}, header.Values(authheaders.DefaultGroupHeader))
	require.Equal(t, []string{"cluster:root"},
		header.Values(authheaders.DefaultExtraHeaderPrefix+"authentication.kcp.io%2Fscopes"),
		"extras carry warrants and scopes, which the far end needs to authorize as this user")
}

// A client that sets the headers itself must not be believed: the far end
// trusts them because of the connection they arrive on, so anything a client
// sent has to be replaced, not merged.
func TestStripsClientSuppliedIdentity(t *testing.T) {
	t.Parallel()

	ctx := genericapirequest.WithUser(t.Context(), &user.DefaultInfo{Name: "alice"})
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://shard.example.com/apis/s3.dev/v1alpha1/bucketinfos", http.NoBody)
	req.Header.Set(authheaders.DefaultUserHeader, "system:masters-please")
	req.Header.Add(authheaders.DefaultGroupHeader, "system:masters")
	req.Header.Set(authheaders.DefaultExtraHeaderPrefix+"smuggled", "yes")

	header := forward(t, req)

	require.Equal(t, "alice", header.Get(authheaders.DefaultUserHeader))
	require.Empty(t, header.Values(authheaders.DefaultGroupHeader),
		"a group the client asked for must not survive")
	require.Empty(t, header.Values(authheaders.DefaultExtraHeaderPrefix+"smuggled"))
}

// An unauthenticated request should not reach here, but if one does the headers
// must be cleared rather than passed through.
func TestStripsIdentityWhenThereIsNone(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://shard.example.com/apis/s3.dev/v1alpha1/bucketinfos", http.NoBody)
	req.Header.Set(authheaders.DefaultUserHeader, "nobody")

	header := forward(t, req)

	require.Empty(t, header.Values(authheaders.DefaultUserHeader))
}
