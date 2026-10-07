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

package mounts

import (
	"net/http"
	"strconv"
)

const (
	// HopsHeader counts how many mount targets a request has already been
	// forwarded to. A mount target may be kcp itself (that is how a workspace is
	// mounted onto another workspace), so a mount can be pointed at a path that
	// resolves back to a mount, directly or through a chain. Without a bound such
	// a request would be forwarded shard to target to shard indefinitely, holding
	// a connection at every hop.
	//
	// The mount proxy replaces this header on every hop, so a value a client sends
	// is overwritten on the first hop and can only ever deny that client's own
	// request. A value arriving from kcp is therefore the real hop count. A mount
	// target that is not kcp can strip the header, which only permits loops that
	// pass through that target.
	//
	// Components that are never a legitimate mount target reject any request
	// carrying this header outright.
	HopsHeader = "X-Kcp-Mount-Hops"

	// MaxHops is how many mount hops a single request may take. Chaining a mount
	// onto a mounted workspace is legitimate, so this allows a short chain and
	// rejects anything longer as a loop.
	MaxHops = 3
)

// HopsFrom returns how many mount targets the request has already been
// forwarded to. A missing or unparsable value counts as none, which is what a
// request that has not passed through a mount proxy yet looks like.
func HopsFrom(header http.Header) int {
	hops, err := strconv.Atoi(header.Get(HopsHeader))
	if err != nil || hops < 0 {
		return 0
	}
	return hops
}

// IsForwardedByMountProxy reports whether the request arrived through a mount
// proxy, i.e. it carries the hop header.
func IsForwardedByMountProxy(header http.Header) bool {
	_, found := header[http.CanonicalHeaderKey(HopsHeader)]
	return found
}
