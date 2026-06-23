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

// Package authheaders contains helpers for stamping the request-header
// identity headers (X-Remote-User / X-Remote-Group / X-Remote-Extra-*) that
// kcp proxies use to forward an authenticated identity to a backend shard or
// virtual workspace. The backend trusts these headers verbatim over the
// proxy's mutually-authenticated connection, so the stamping must always strip
// any client-supplied copies first.
package authheaders

import (
	"net/http"
	"net/url"
	"strings"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
)

// SetAuthHeaders stamps the given authenticated user's identity onto the request
// headers, after deleting any inbound copies of the identity headers.
//
// This mirrors k8s.io/client-go/transport.SetAuthProxyHeaders and the upstream
// requestheader authenticator's ClearAuthenticationHeaders.
func SetAuthHeaders(header http.Header, user userinfo.Info, userHeader, groupHeader, extraHeaderPrefix string) {
	header.Del(userHeader)
	header.Del(groupHeader)
	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), strings.ToLower(extraHeaderPrefix)) {
			header.Del(key)
		}
	}

	header.Set(userHeader, user.GetName())

	for _, group := range user.GetGroups() {
		header.Add(groupHeader, group)
	}

	for k, values := range user.GetExtra() {
		// Key must be encoded to enable e.g authentication.kcp.io/cluster-name
		// This is decoded in the RequestHeader auth handler.
		encodedKey := url.PathEscape(k)
		for _, v := range values {
			header.Add(extraHeaderPrefix+encodedKey, v)
		}
	}
}
