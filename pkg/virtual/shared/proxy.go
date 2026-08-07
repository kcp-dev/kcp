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
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"github.com/kcp-dev/kcp/pkg/shardlookup"
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

type idleConnectionsCloser interface {
	CloseIdleConnections()
}

// closeIdleConnections closes the idle connections held by rt's underlying
// http.Transport and reports whether it found one. The round-trippers built by
// rest.TransportFor do not implement the function, so unwrap them until
// an http.Transport is found.
func closeIdleConnections(rt http.RoundTripper) bool {
	for {
		if closer, ok := rt.(idleConnectionsCloser); ok {
			closer.CloseIdleConnections()
			return true
		}
		wrapper, ok := rt.(utilnet.RoundTripperWrapper)
		if !ok {
			return false
		}
		rt = wrapper.WrappedRoundTripper()
		if rt == nil {
			return false
		}
	}
}

// TransportCache caches impersonating round-trippers so that long-lived
// initializing and terminating workspaces do not rebuild an http.Transport
// on every request. TTLCache allows for concurrent requests on the same
// key to be enqueued. Idle connections are closed once an entry expires.
type TransportCache struct {
	cfg   *rest.Config
	cache *shardlookup.TTLCache[http.RoundTripper]
}

// NewTransportCache creates a TransportCache that derives transports from cfg.
func NewTransportCache(cfg *rest.Config) *TransportCache {
	c := &TransportCache{
		cfg:   cfg,
		cache: shardlookup.NewTTLCache[http.RoundTripper](),
	}
	c.cache.OnEviction(func(rt http.RoundTripper) {
		_ = closeIdleConnections(rt)
	})
	return c
}

// StartWithContext starts the background eviction goroutine and stops it when
// ctx is cancelled.
func (c *TransportCache) StartWithContext(ctx context.Context) {
	c.cache.StartWithContext(ctx)
}

// TransportFor returns a cached or new round-tripper for the given impersonation config.
func (c *TransportCache) TransportFor(imp rest.ImpersonationConfig) (http.RoundTripper, error) {
	return c.cache.Get(impersonationKey(imp), func() (http.RoundTripper, error) {
		cfg := rest.CopyConfig(c.cfg)
		cfg.Impersonate = imp
		return rest.TransportFor(cfg)
	})
}

const impersonationKeySeparator = "\x00"

// impersonationKey returns a string that uniquely identifies the given
// impersonation config. \x00 (NUL) separator is used as it cannot be in
// any of the values. This is faster and less memory than doing something
// like: sorting, marshaling to JSON, and hashing.
func impersonationKey(imp rest.ImpersonationConfig) string {
	var b strings.Builder
	b.WriteString("u=")
	b.WriteString(imp.UserName)
	b.WriteString(impersonationKeySeparator)
	b.WriteString("uid=")
	b.WriteString(imp.UID)

	groups := append([]string(nil), imp.Groups...)
	sort.Strings(groups)
	for _, g := range groups {
		b.WriteString(impersonationKeySeparator)
		b.WriteString("g=")
		b.WriteString(g)
	}

	keys := make([]string, 0, len(imp.Extra))
	for k := range imp.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := append([]string(nil), imp.Extra[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			b.WriteString(impersonationKeySeparator)
			b.WriteString("e=")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(v)
		}
	}

	return b.String()
}
