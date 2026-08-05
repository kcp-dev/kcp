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
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

// authFunc adapts a function to authenticator.Request.
type authFunc func(req *http.Request) (*authenticator.Response, bool, error)

func (f authFunc) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	return f(req)
}

// authzFunc adapts a function to authorizer.Authorizer.
type authzFunc func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error)

func (f authzFunc) Authorize(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	return f(ctx, a)
}

// TestWithMetricsAuthorization verifies that the /metrics guard rejects
// anonymous callers (401) and authenticated-but-unauthorized callers (403), and
// only invokes the wrapped handler for an authenticated, authorized caller.
func TestWithMetricsAuthorization(t *testing.T) {
	t.Parallel()
	authenticated := authFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		return &authenticator.Response{User: &user.DefaultInfo{Name: "scraper"}}, true, nil
	})
	anonymous := authFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		return nil, false, nil
	})
	allow := authzFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		// The guard must authorize the /metrics non-resource URL.
		if a.IsResourceRequest() || a.GetPath() != "/metrics" || a.GetVerb() != "get" {
			return authorizer.DecisionDeny, "unexpected attributes", nil
		}
		return authorizer.DecisionAllow, "", nil
	})
	deny := authzFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		return authorizer.DecisionNoOpinion, "nope", nil
	})

	for _, tc := range []struct {
		name       string
		auth       authenticator.Request
		authz      authorizer.Authorizer
		wantStatus int
		wantServed bool
	}{
		{name: "anonymous is rejected", auth: anonymous, authz: allow, wantStatus: http.StatusUnauthorized, wantServed: false},
		{name: "authenticated but unauthorized is forbidden", auth: authenticated, authz: deny, wantStatus: http.StatusForbidden, wantServed: false},
		{name: "authenticated and authorized is served", auth: authenticated, authz: allow, wantStatus: http.StatusOK, wantServed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			served := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served = true
				w.WriteHeader(http.StatusOK)
			})

			h := withMetricsAuthorization(inner, tc.auth, tc.authz, requestinfo.NewFactory())

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if served != tc.wantServed {
				t.Errorf("wrapped handler served = %v, want %v", served, tc.wantServed)
			}
		})
	}
}
