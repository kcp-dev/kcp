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

// Package longrunning decides which requests are exempt from the apiserver's
// request timeout.
package longrunning

import (
	"net/http"
	"strings"

	apirequest "k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// WithStreamingCustomSubresources extends a long-running check so that a
// protocol upgrade on a custom subresource counts as long-running.
//
// The stock check knows streaming subresources by name: attach, exec, proxy,
// log and portforward. An APIExport may declare a subresource under any name,
// "linuxservers/ssh" say, and without this the request timeout filter gives
// such a session a hard deadline, cutting it off mid-stream however active it
// is.
//
// Only upgrade requests are exempted. An upgrade is the client saying the
// connection will be taken over for a stream, which a deadline meant for
// request/response calls cannot sensibly bound; an ordinary call to a custom
// subresource stays subject to the timeout like any other. status and scale are
// never custom subresources, so they are left to the base check.
//
// This runs before the subresource is known to be declared by any APIExport. A
// request for an undeclared one is exempted too, and is then refused by whatever
// serves the resource.
func WithStreamingCustomSubresources(base apirequest.LongRunningRequestCheck) apirequest.LongRunningRequestCheck {
	return func(r *http.Request, requestInfo *apirequest.RequestInfo) bool {
		if base != nil && base(r, requestInfo) {
			return true
		}
		if requestInfo == nil || !requestInfo.IsResourceRequest {
			return false
		}
		if requestInfo.Subresource == "" || apisv1alpha2.IsSchemaOwnedSubresource(requestInfo.Subresource) {
			return false
		}
		return isUpgradeRequest(r)
	}
}

// isUpgradeRequest reports whether the request asks for a protocol upgrade. It
// matches httpstream.IsUpgradeRequest.
func isUpgradeRequest(r *http.Request) bool {
	for _, h := range r.Header[http.CanonicalHeaderKey("Connection")] {
		if strings.Contains(strings.ToLower(h), "upgrade") {
			return true
		}
	}
	return false
}
