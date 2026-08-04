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

// Package hops bounds how far a request may travel between a shard and the
// virtual workspaces serving its virtual resources.
//
// A shard serves a virtual resource by forwarding to the virtual workspace
// advertised for it, and a virtual workspace may serve a resource by delegating
// back to the shard. Both are legitimate, and chaining them is legitimate too:
//
//	APIExport VW -> shard -> Replication VW -> cache server        terminates
//
// But the same two moves also compose into a cycle, when the virtual workspace
// a request is forwarded to is the one that delegated it in the first place:
//
//	APIExport VW -> shard -> APIExport VW -> shard -> ...           spins
//
// Nothing static distinguishes the two. The export, the storage type and the
// endpoint URL are identical in both; only what the far end does with the
// request differs, and that is not knowable before sending it. So instead of
// trying to detect the cycle, this counts how deep a request has gone and
// refuses to go deeper than a legitimate chain ever needs.
//
// Counting only works if every leg carries the count. An HTTP proxy forwards
// headers on its own, but a virtual workspace that delegates through a
// client-go client starts a fresh request, so the count has to survive as
// context: WithRequestHops puts an inbound header into the context, and
// WrapConfig puts the context value back onto outbound requests.
package hops

import (
	"context"
	"net/http"
	"strconv"

	"k8s.io/client-go/rest"
)

// Header carries how many times a request has been forwarded.
//
// A client may set it, but only against itself: an inflated value fails that
// one request, which is why it is not stripped at the edge.
const Header = "X-Kcp-Virtual-Resource-Hops"

// Max is how deep forwarding may go before a request is refused.
//
// The longest legitimate chain observed is two hops -- a client reaching an
// APIExport virtual workspace, which delegates to the shard, which forwards to
// the replication virtual workspace. The limit leaves room above that; a cycle
// exceeds any finite limit.
const Max = 4

type contextKey int

const hopsKey contextKey = iota

// FromHeader reports how many times the request carrying these headers has
// been forwarded. Anything unparsable counts as Max, so that a malformed header
// cannot buy more hops than a well-formed one.
func FromHeader(h http.Header) int {
	raw := h.Get(Header)
	if raw == "" {
		return 0
	}
	hops, err := strconv.Atoi(raw)
	if err != nil || hops < 0 {
		return Max
	}
	return hops
}

// SetHeader records a hop count on an outgoing request.
func SetHeader(h http.Header, hops int) {
	h.Set(Header, strconv.Itoa(hops))
}

// WithHops returns a context carrying a hop count.
func WithHops(ctx context.Context, hops int) context.Context {
	return context.WithValue(ctx, hopsKey, hops)
}

// FromContext reports the hop count carried by a context, zero if none is.
func FromContext(ctx context.Context) int {
	hops, ok := ctx.Value(hopsKey).(int)
	if !ok {
		return 0
	}
	return hops
}

// Exceeded reports whether a request that has been forwarded this many times
// has reached the limit and must not be forwarded again.
func Exceeded(hops int) bool {
	return hops >= Max
}

// WithRequestHops records the inbound hop count in the request context, so that
// work done on behalf of this request can carry it onwards.
func WithRequestHops(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hops := FromHeader(req.Header)
		if hops > 0 {
			req = req.WithContext(WithHops(req.Context(), hops))
		}
		handler.ServeHTTP(w, req)
	})
}

// WrapConfig makes clients built from cfg carry the hop count of the request
// they are serving. Without it a virtual workspace that answers by delegating
// through a client-go client would restart the count at zero on every lap of a
// cycle, and the count would never reach its limit.
func WrapConfig(cfg *rest.Config) *rest.Config {
	cfg = rest.CopyConfig(cfg)
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return roundTripper{delegate: rt}
	})
	return cfg
}

type roundTripper struct {
	delegate http.RoundTripper
}

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if hops := FromContext(req.Context()); hops > 0 {
		req = req.Clone(req.Context())
		SetHeader(req.Header, hops)
	}
	return r.delegate.RoundTrip(req)
}
