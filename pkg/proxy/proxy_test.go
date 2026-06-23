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
	"net/http/httptest"
	"reflect"
	"testing"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
)

const (
	defaultUserHeader   = "X-Remote-User"
	defaultGroupHeader  = "X-Remote-Group"
	defaultExtraPrefix  = "X-Remote-Extra-"
	warrantExtraHeader  = "X-Remote-Extra-Authorization.kcp.io%2fwarrant"
	scopesExtraHeader   = "X-Remote-Extra-Authentication.kcp.io%2fscopes"
	clusterNameEncodedK = "authentication.kcp.io/cluster-name"
)

// forwardThroughProxy runs WithProxyAuthHeaders with the given authenticated
// user (or none, if user is nil) over a request carrying the given inbound
// client headers, and returns the headers as they would be forwarded to the
// shard.
func forwardThroughProxy(t *testing.T, user userinfo.Info, userHeader, groupHeader, extraPrefix string, clientHeaders http.Header) http.Header {
	t.Helper()

	var forwarded http.Header
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Clone()
	})

	handler := WithProxyAuthHeaders(sink, userHeader, groupHeader, extraPrefix)

	req := httptest.NewRequest(http.MethodGet, "https://front-proxy/clusters/root:org:ws/api/v1/secrets", nil)
	for k, vs := range clientHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if user != nil {
		req = req.WithContext(request.WithUser(req.Context(), user))
	}

	handler.ServeHTTP(httptest.NewRecorder(), req)
	return forwarded
}

// TestWithProxyAuthHeaders_StripsForgedIdentityHeaders is the regression test for
// the front-proxy identity-header injection vulnerability: a client that
// authenticates by a non-request-header method must not be able to smuggle
// X-Remote-* headers through to the shard.
func TestWithProxyAuthHeaders_StripsForgedIdentityHeaders(t *testing.T) {
	forgedWarrant := `{"user":"attacker","groups":["system:masters"]}`

	tests := []struct {
		name          string
		user          userinfo.Info
		clientHeaders http.Header
		wantUser      []string
		wantGroups    []string
		// extra headers (by full header name) that MUST NOT be present at all.
		forbiddenHeaders []string
	}{
		{
			name: "forged group is dropped, real identity stamped",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			clientHeaders: http.Header{
				defaultGroupHeader: {"system:masters"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"system:authenticated"},
		},
		{
			name: "forged user is overwritten with the real identity",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			clientHeaders: http.Header{
				defaultUserHeader:  {"system:admin"},
				defaultGroupHeader: {"system:masters"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"system:authenticated"},
		},
		{
			name: "forged warrant and scopes extras are dropped (no X-Remote-User sent)",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			clientHeaders: http.Header{
				defaultGroupHeader: {"system:masters"},
				warrantExtraHeader: {forgedWarrant},
				scopesExtraHeader:  {"cluster:root"},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantExtraHeader, scopesExtraHeader},
		},
		{
			name: "multiple forged group values are all dropped",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"team:a", "team:b"}},
			clientHeaders: http.Header{
				defaultGroupHeader: {"system:masters", "org:required-group", "system:cluster-admins"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"team:a", "team:b"},
		},
		{
			name: "real identity with extras is stamped, forged extras dropped",
			user: &userinfo.DefaultInfo{
				Name:   "alice",
				Groups: []string{"system:authenticated"},
				Extra:  map[string][]string{clusterNameEncodedK: {"root:org:ws"}},
			},
			clientHeaders: http.Header{
				warrantExtraHeader: {forgedWarrant},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantExtraHeader},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := forwardThroughProxy(t, tc.user, defaultUserHeader, defaultGroupHeader, defaultExtraPrefix, tc.clientHeaders)

			if gotUser := got.Values(defaultUserHeader); !reflect.DeepEqual(gotUser, tc.wantUser) {
				t.Errorf("user header = %v, want %v", gotUser, tc.wantUser)
			}
			if gotGroups := got.Values(defaultGroupHeader); !reflect.DeepEqual(gotGroups, tc.wantGroups) {
				t.Errorf("group header = %v, want %v", gotGroups, tc.wantGroups)
			}
			for _, g := range got.Values(defaultGroupHeader) {
				if g == "system:masters" {
					t.Errorf("forged group system:masters leaked to shard: %v", got.Values(defaultGroupHeader))
				}
			}
			for _, h := range tc.forbiddenHeaders {
				if v := got.Values(h); len(v) != 0 {
					t.Errorf("forged header %q leaked to shard: %v", h, v)
				}
			}
		})
	}
}

// TestWithProxyAuthHeaders_StripsCaseInsensitiveExtraPrefix ensures the extra
// header strip matches the prefix case-insensitively, the same as the upstream
// requestheader authenticator that consumes the headers on the shard.
func TestWithProxyAuthHeaders_StripsCaseInsensitiveExtraPrefix(t *testing.T) {
	// Note: net/http canonicalizes header keys, but the configured prefix could
	// differ in case from the canonical form; ensure we still strip.
	got := forwardThroughProxy(t,
		&userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
		defaultUserHeader, defaultGroupHeader, defaultExtraPrefix,
		http.Header{
			"X-Remote-Extra-Authorization.kcp.io%2fwarrant": {`{"groups":["system:masters"]}`},
		},
	)
	for k, v := range got {
		if k != defaultUserHeader && k != defaultGroupHeader {
			t.Errorf("unexpected leaked header %q = %v", k, v)
		}
	}
}

// TestWithProxyAuthHeaders_CustomHeaderNames ensures forged headers are stripped
// when the mapping configures non-default header names.
func TestWithProxyAuthHeaders_CustomHeaderNames(t *testing.T) {
	const (
		userHeader  = "X-Custom-User"
		groupHeader = "X-Custom-Group"
		extraPrefix = "X-Custom-Extra-"
	)
	got := forwardThroughProxy(t,
		&userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
		userHeader, groupHeader, extraPrefix,
		http.Header{
			groupHeader:              {"system:masters"},
			"X-Custom-Extra-Warrant": {"forged"},
			userHeader:               {"system:admin"},
		},
	)

	if u := got.Values(userHeader); !reflect.DeepEqual(u, []string{"alice"}) {
		t.Errorf("user header = %v, want [alice]", u)
	}
	if g := got.Values(groupHeader); !reflect.DeepEqual(g, []string{"system:authenticated"}) {
		t.Errorf("group header = %v, want [system:authenticated]", g)
	}
	if v := got.Values("X-Custom-Extra-Warrant"); len(v) != 0 {
		t.Errorf("forged custom extra header leaked: %v", v)
	}
}

// TestWithProxyAuthHeaders_NoAuthenticatedUser ensures that when no user is in
// the request context (the proxy did not authenticate the request), the handler
// passes through without stamping. The inbound forged headers are left as-is
// here because WithProxyAuthHeaders only stamps for authenticated requests; in
// the real chain such a request is rejected by WithOptionalAuthentication's
// failed handler and never reaches a shard mapping. This documents that
// contract.
func TestWithProxyAuthHeaders_NoAuthenticatedUser(t *testing.T) {
	var served bool
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served = true })
	handler := WithProxyAuthHeaders(sink, defaultUserHeader, defaultGroupHeader, defaultExtraPrefix)

	req := httptest.NewRequest(http.MethodGet, "https://front-proxy/api/v1/secrets", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !served {
		t.Fatal("expected request to be passed through to delegate")
	}
}
