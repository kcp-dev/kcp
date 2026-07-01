/*
Copyright 2022 The kcp Authors.

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

package filters

import (
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/authentication"
)

// WithOptionalAuthentication creates a handler that authenticates
// a request if credentials are presented but passes through to the next
// handler if one is not.
func WithOptionalAuthentication(handler, failed http.Handler, auth authenticator.Request) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Collect all available authenticators: base (client cert, token file, etc.)
		// plus any per-workspace authenticator resolved earlier in the chain.
		authenticators := make([]authenticator.Request, 0, 2)
		if auth != nil {
			authenticators = append(authenticators, auth)
		}

		if wsAuth, ok := authentication.WorkspaceAuthenticatorFrom(req.Context()); ok {
			authenticators = append(authenticators, wsAuth)
		}

		// No authenticators configured
		if len(authenticators) == 0 {
			handler.ServeHTTP(w, req)
			return
		}

		// Try to authenticate the request
		combined := authenticatorunion.New(authenticators...)
		resp, ok, err := combined.AuthenticateRequest(req)
		if err == nil && ok {
			// Add identity if authentication passed
			req = req.WithContext(request.WithUser(req.Context(), resp.User))
			// If this failed the request has to propagate to the shard.
			// The request might be made with a static token which the
			// front-proxy has no knowledge of.
			// This will be superfluous in the future, see
			// https://github.com/kcp-dev/kcp/issues/4100
		}
		handler.ServeHTTP(w, req)
	})
}

func NewUnauthorizedHandler() http.Handler {
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)
	return genericapifilters.Unauthorized(codecs)
}
