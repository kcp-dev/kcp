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

package revdial

import (
	"context"
)

type tunnelIDKey int

const tunnelIDContextKey tunnelIDKey = iota

// WithTunnelID puts the request TunnelID into the current context.
func WithTunnelID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tunnelIDContextKey, id)
}

// TunnelIDFrom returns the request TunnelID from the context.
// A zero ID is returned if there are no identifiers in the current context.
func TunnelIDFrom(ctx context.Context) string {
	v := ctx.Value(tunnelIDContextKey)
	if v == nil {
		return ""
	}
	return v.(string)
}

type tunnelPathKey int

const tunnelPathContextKey tunnelPathKey = iota

// WithTunnelPath puts the request Path to be tunneled into the current context.
func WithTunnelPath(ctx context.Context, tunnelPath string) context.Context {
	return context.WithValue(ctx, tunnelPathContextKey, tunnelPath)
}

// TunnelPathFrom puts returns the requested Path to be tunneled from the context.
func TunnelPathFrom(ctx context.Context) string {
	v := ctx.Value(tunnelPathContextKey)
	if v == nil {
		return ""
	}
	return v.(string)
}
