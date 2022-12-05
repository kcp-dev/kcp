/*
Copyright 2022 The KCP Authors.

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

package tenancy

import (
	"context"
	"net/http"

	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	// XKcpCanonicalPathHeader is a header injected by the front-proxy containing
	// the colon separated path of the workspace name, e.g. root:foo:bar or
	// sd6af9asd8:foo:bar for another hierarchy subtree.
	//
	// WARNING: only ever use this header for defaulting of values the client could
	// easily specify themselves directly in the API objects.
	XKcpCanonicalPathHeader = "X-kcp-workspace-path"
)

// CanonicalPathFromHeader returns a canonical workspace path from the http headers.
//
// WARNING: only ever use this header for defaulting of values the client could
// easily specify themselves directly in the API objects.
func CanonicalPathFromHeader(headers http.Header) (logicalcluster.Name, bool) {
	if values, ok := headers[XKcpCanonicalPathHeader]; !ok {
		return logicalcluster.Name{}, false
	} else {
		return logicalcluster.New(values[0]), true
	}
}

type canonicalPathKey int

const canonicalPathContextKey canonicalPathKey = iota

// WithCanonicalPath returns a context with the canonical workspace path.
func WithCanonicalPath(parent context.Context, canonicalPath logicalcluster.Name) context.Context {
	return context.WithValue(parent, canonicalPathContextKey, canonicalPath)
}

// CanonicalPathFrom returns a canonical workspace path from the context.
//
// WARNING: only ever use this header for defaulting of values the client could
// easily specify themselves directly in the API objects.
func CanonicalPathFrom(ctx context.Context) logicalcluster.Name {
	canonicalPath, ok := ctx.Value(canonicalPathContextKey).(logicalcluster.Name)
	if !ok {
		return logicalcluster.Name{}
	}
	return canonicalPath
}
