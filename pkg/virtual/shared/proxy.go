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

// Package shared contains helpers reused by the initializing and terminating
// workspace virtual workspaces. Both VWs implement the same lifecycle content
// proxy pattern, so they share their HTTP plumbing here to keep behaviour in
// lockstep.
package shared

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"k8s.io/client-go/transport"
)

// ServeProxy strips any client-supplied auth/impersonation headers from the
// request and reverse-proxies it to forwardedHost using the supplied
// (impersonating) transport. It is used by the workspace-content sub-workspace
// handlers under both modes of operation (synthetic-group + caller identity,
// or owner impersonation fallback).
func ServeProxy(writer http.ResponseWriter, request *http.Request, forwardedHost *url.URL, rt http.RoundTripper) {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			for _, header := range []string{
				"Authorization",
				transport.ImpersonateUserHeader,
				transport.ImpersonateUIDHeader,
				transport.ImpersonateGroupHeader,
			} {
				req.Header.Del(header)
			}
			for key := range req.Header {
				if strings.HasPrefix(key, transport.ImpersonateUserExtraHeaderPrefix) {
					req.Header.Del(key)
				}
			}
			req.URL.Scheme = forwardedHost.Scheme
			req.URL.Host = forwardedHost.Host
		},
		Transport: rt,
	}
	proxy.ServeHTTP(writer, request)
}
