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

package authheaders

import (
	"net/http"
	"reflect"
	"testing"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
)

const (
	userHeader  = "X-Remote-User"
	groupHeader = "X-Remote-Group"
	extraPrefix = "X-Remote-Extra-"

	warrantHeader = "X-Remote-Extra-Authorization.kcp.io%2fwarrant"
	scopesHeader  = "X-Remote-Extra-Authentication.kcp.io%2fscopes"
)

// TestSetAuthHeaders_StripsForgedIdentityHeaders is the regression test for the
// identity-header injection vulnerability: client-supplied X-Remote-* headers
// must be removed before the authenticated identity is stamped, so a forged
// value can never be forwarded to a backend that trusts request-header auth.
func TestSetAuthHeaders_StripsForgedIdentityHeaders(t *testing.T) {
	forgedWarrant := `{"user":"attacker","groups":["system:masters"]}`

	tests := []struct {
		name             string
		user             userinfo.Info
		inbound          http.Header
		wantUser         []string
		wantGroups       []string
		forbiddenHeaders []string
	}{
		{
			name: "forged group dropped, real identity stamped",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			inbound: http.Header{
				groupHeader: {"system:masters"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"system:authenticated"},
		},
		{
			name: "forged user overwritten, forged warrant/scopes dropped (no user header sent)",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			inbound: http.Header{
				groupHeader:   {"system:masters", "org:required"},
				warrantHeader: {forgedWarrant},
				scopesHeader:  {"cluster:root"},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantHeader, scopesHeader},
		},
		{
			name: "forged user header is overwritten with the real identity",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"team:a"}},
			inbound: http.Header{
				userHeader:  {"system:admin"},
				groupHeader: {"system:masters"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"team:a"},
		},
		{
			name: "real extras are stamped (encoded), forged extras dropped",
			user: &userinfo.DefaultInfo{
				Name:   "alice",
				Groups: []string{"system:authenticated"},
				Extra:  map[string][]string{"authentication.kcp.io/cluster-name": {"root:org:ws"}},
			},
			inbound: http.Header{
				warrantHeader: {forgedWarrant},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantHeader},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.inbound.Clone()
			if h == nil {
				h = http.Header{}
			}

			SetAuthHeaders(h, tc.user, userHeader, groupHeader, extraPrefix)

			if got := h.Values(userHeader); !reflect.DeepEqual(got, tc.wantUser) {
				t.Errorf("user header = %v, want %v", got, tc.wantUser)
			}
			if got := h.Values(groupHeader); !reflect.DeepEqual(got, tc.wantGroups) {
				t.Errorf("group header = %v, want %v", got, tc.wantGroups)
			}
			for _, g := range h.Values(groupHeader) {
				if g == "system:masters" {
					t.Errorf("forged group system:masters leaked: %v", h.Values(groupHeader))
				}
			}
			for _, name := range tc.forbiddenHeaders {
				if v := h.Values(name); len(v) != 0 {
					t.Errorf("forged header %q leaked: %v", name, v)
				}
			}
		})
	}
}

// TestSetAuthHeaders_RealExtraIsEncodedAndForwarded confirms the legitimate
// extras round-trip through the PathEscape encoding the request-header
// authenticator expects.
func TestSetAuthHeaders_RealExtraIsEncodedAndForwarded(t *testing.T) {
	h := http.Header{}
	SetAuthHeaders(h, &userinfo.DefaultInfo{
		Name:  "alice",
		Extra: map[string][]string{"authentication.kcp.io/cluster-name": {"root:org:ws"}},
	}, userHeader, groupHeader, extraPrefix)

	const encoded = "X-Remote-Extra-Authentication.kcp.io%2Fcluster-name"
	if got := h.Values(encoded); !reflect.DeepEqual(got, []string{"root:org:ws"}) {
		t.Errorf("encoded extra header %q = %v, want [root:org:ws]; full headers=%v", encoded, got, h)
	}
}
