/*
Copyright 2025 The KCP Authors.

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

package authentication

import (
	"context"
	"net/http"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/group"

	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

// WithWorkspaceAuthResolver looks up the target cluster in the given auth index
// to populate a possible workspace authenticator in the request's context. This
// is used to let other middlewares know about the existence of additional auth
// options.
func WithWorkspaceAuthResolver(handler http.Handler, authIndex AuthenticatorIndex) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		wsType := lookup.WorkspaceTypeFrom(req.Context())
		if wsType.Empty() {
			handler.ServeHTTP(w, req)
			return
		}

		authn, ok := authIndex.Lookup(wsType)
		if !ok {
			handler.ServeHTTP(w, req)
			return
		}

		authn = group.NewAuthenticatedGroupAdder(authn)

		// make the authenticator always add the target cluster to the user scopes
		authn = withClusterScope(authn)

		req = req.WithContext(WithWorkspaceAuthenticator(req.Context(), authn))
		handler.ServeHTTP(w, req)
	})
}

type contextKey int

const (
	authenticatorContextKey contextKey = iota
)

func WithWorkspaceAuthenticator(parent context.Context, authenticator authenticator.Request) context.Context {
	return context.WithValue(parent, authenticatorContextKey, authenticator)
}

func WorkspaceAuthenticatorFrom(ctx context.Context) (authenticator.Request, bool) {
	authenticator, ok := ctx.Value(authenticatorContextKey).(authenticator.Request)
	if !ok {
		return nil, false
	}
	return authenticator, true
}
