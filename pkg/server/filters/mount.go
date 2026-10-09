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

package filters

import (
	"context"
	"net/http"
	"net/url"

	"github.com/kcp-dev/logicalcluster/v3"
)

type mountTargetContextKeyType int

const mountTargetContextKey mountTargetContextKeyType = iota

// MountTarget describes where a request for a mounted workspace has to be
// forwarded. It is resolved by the local proxy in front of the handler chain
// and consumed by the mount proxy filter after authentication, so that mount
// traffic is authenticated, audited and flow-controlled like any other
// request before it leaves the shard.
type MountTarget struct {
	// URL is the validated mount target (spec.URL of the mounted Workspace).
	URL *url.URL
	// Workspace is the full path of the mounted workspace as it was requested.
	Workspace logicalcluster.Path
	// ParentCluster is the logical cluster that holds the Workspace object
	// describing the mount, i.e. the parent of Workspace.
	ParentCluster logicalcluster.Name
}

// WithMountTarget stores the mount target of the request in the context.
func WithMountTarget(parent context.Context, target *MountTarget) context.Context {
	return context.WithValue(parent, mountTargetContextKey, target)
}

// MountTargetFrom returns the mount target of the request, or nil if the
// request is not for a mounted workspace.
func MountTargetFrom(ctx context.Context) *MountTarget {
	target, _ := ctx.Value(mountTargetContextKey).(*MountTarget)
	return target
}

// WithMountBypass sends requests for mounted workspaces straight into chain
// and everything else through mux. The mux in front of the shard's handler
// chain registers path prefixes (e.g. /services/ for the embedded virtual
// workspace server) that must not capture a request whose remaining path, after
// the mounted workspace prefix was stripped, happens to start with one of them.
func WithMountBypass(mux, chain http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if MountTargetFrom(req.Context()) != nil {
			chain.ServeHTTP(w, req)
			return
		}
		mux.ServeHTTP(w, req)
	})
}
