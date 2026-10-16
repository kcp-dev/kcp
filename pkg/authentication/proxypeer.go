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

package authentication

import (
	"net/http"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/authenticatorfactory"
	x509request "k8s.io/apiserver/pkg/authentication/request/x509"
)

// WithoutProxyPeerIdentity prevents an authenticating proxy's client certificate from authenticating as a user.
func WithoutProxyPeerIdentity(delegate authenticator.Request, requestHeader *authenticatorfactory.RequestHeaderConfig) authenticator.Request {
	// Reuses upstream requestheader peer verification (CA and allowed names) as a predicate.
	proxyPeer := x509request.NewDynamicCAVerifier(
		requestHeader.CAContentProvider.VerifyOptions,
		authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
			return nil, true, nil
		}),
		requestHeader.AllowedClientNames,
	)

	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		_, isProxyPeer, err := proxyPeer.AuthenticateRequest(req)
		if err != nil || !isProxyPeer {
			// request is not coming from proxy, validate fully
			//
			// an error is also returned when e.g. the offered
			// certificate doesn't contain the allowed client names
			return delegate.AuthenticateRequest(req)
		}

		if hasHeader(req.Header, requestHeader.UsernameHeaders.Value()) {
			// the request has the header set and comes from the proxy
			// the header is only set by the proxy for cert auth, which
			// cannot be proxied
			return delegate.AuthenticateRequest(req)
		}

		// username is not set in the header, drop the proxy's TLS
		// information and pass through the chain for e.g. OIDC
		tlsState := *req.TLS
		tlsState.PeerCertificates = nil
		tlsState.VerifiedChains = nil
		withoutPeer := req.WithContext(req.Context())
		withoutPeer.TLS = &tlsState
		return delegate.AuthenticateRequest(withoutPeer)
	})
}

// hasHeader reports whether any of names has a non-empty value in h.
func hasHeader(h http.Header, names []string) bool {
	for _, name := range names {
		if h.Get(name) != "" {
			return true
		}
	}
	return false
}
