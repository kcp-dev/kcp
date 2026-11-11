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

// Package impersonation deals with the impersonation headers a client may put
// on a request, from the point of view of something that forwards requests.
//
// A virtual workspace commonly answers a request by forwarding it to a shard
// under an identity it established itself, either the caller's identity carried
// via impersonation, or a privileged identity of its own. The caller's request
// may itself carry impersonation headers, asking to act as someone else. Those
// were evaluated against the caller at the edge; if they travelled on to the
// shard, the shard would evaluate them again, this time against the forwarding
// identity, which is typically allowed far more than the caller is.
//
// So anything that forwards a request must do one of two things: refuse the
// request when it asks to impersonate (HasHeaders), or strip the impersonation
// headers before forwarding (ScrubHeaders). Which one is right depends on
// whether the forwarder can honour the impersonation itself.
package impersonation

import (
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
)

// HasHeaders reports whether the request asks to impersonate someone, through
// any of the headers the apiserver's impersonation filter acts on. All of them
// must be considered: a request that only sets the UID header is still an
// impersonation request as far as that filter is concerned, so treating it as
// unimpersonated here would let it skip whatever checks the caller makes.
//
// A header counts when it carries at least one non-empty value.
func HasHeaders(header http.Header) bool {
	for key, values := range header {
		if !isImpersonationHeader(key) {
			continue
		}
		for _, v := range values {
			if v != "" {
				return true
			}
		}
	}
	return false
}

// ScrubHeaders removes every impersonation header from the request.
//
// Anything that forwards a request onwards under an identity it established
// itself must do this, so that an impersonation the caller asked for is not
// replayed against the backend, where it would be evaluated against the
// forwarded identity instead of the original caller's.
func ScrubHeaders(header http.Header) {
	for key := range header {
		if isImpersonationHeader(key) {
			// Delete by the key as it is stored, rather than http.Header.Del, which
			// would canonicalise it first and so miss a hand-built, non-canonical
			// key that HasHeaders does find.
			delete(header, key)
		}
	}
}

// isImpersonationHeader reports whether key is one of the impersonation headers.
// Keys parsed from the wire are canonical, but a header map can also be built by
// hand, so the comparison ignores case.
func isImpersonationHeader(key string) bool {
	key = strings.ToLower(key)
	switch key {
	case strings.ToLower(authenticationv1.ImpersonateUserHeader),
		strings.ToLower(authenticationv1.ImpersonateUIDHeader),
		strings.ToLower(authenticationv1.ImpersonateGroupHeader):
		return true
	}
	return strings.HasPrefix(key, strings.ToLower(authenticationv1.ImpersonateUserExtraHeaderPrefix))
}
