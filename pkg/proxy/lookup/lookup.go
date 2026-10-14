/*
Copyright 2025 The kcp Authors.

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

package lookup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

func WithClusterResolver(delegate http.Handler, mappings []types.PathMapping, idx index.Index) http.Handler {
	mux := http.NewServeMux()

	// fallback for all unrecognized URLs
	mux.Handle("/", delegate)

	// Mappings are an additional source of cluster names in URLs: a {cluster}
	// placeholder in the mapping path, or failing that a /clusters/<x> segment
	// in the request path, resolves the cluster into the context. The actual
	// muxing to the destinations happens later in proxy.HttpHandler.
	mappingHandler := WithClusterResolutionPattern(delegate, idx, "cluster")

	for _, mapping := range mappings {
		p := strings.TrimRight(mapping.Path, "/")

		// Even though we know how to handle the "special" core clusters path,
		// the mapping provides additional PKI configuration that is not available
		// by just looking up the cluster in the index and figuring out the
		// target shard. That's why it's required to configure /clusters/ in the
		// front-proxy mappings and since admins could choose not to include it,
		// we only enable the built-in clusterResolveHandler if we actually find
		// an appropriate mapping.
		if p == "/clusters" {
			mux.Handle("/clusters/", WithClusterResolution(newShardHandler(delegate, idx), idx))
			continue
		}

		// mappings are configured with *prefixes*; in order to match both exact matches
		// and prefix matches (i.e. if "/foo" is configured, both "/foo" and "/foo/bar"
		// must match), each mapping is added twice to the mux.
		mux.Handle(p, mappingHandler)
		mux.Handle(p+"/{trail...}", mappingHandler)
	}

	return mux
}

// newShardHandler stores the shard URL of the resolved cluster in the
// context and forwards to delegate.
func newShardHandler(delegate http.Handler, idx index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger := klog.FromContext(ctx)

		var segment string
		var clusterName logicalcluster.Name
		switch cluster, mount := request.ClusterFrom(ctx), MountFrom(ctx); {
		case mount != nil:
			// <parentID>:<name>
			_, name := mount.Workspace.Split()
			segment = mount.ParentCluster.Path().Join(name).String()
		case cluster == nil:
			responsewriters.InternalError(w, req, errors.New("no cluster in request context"))
			return
		case cluster.Wildcard:
			// Wildcard requests have no owning shard.
			ForbiddenHandler().ServeHTTP(w, req)
			return
		default:
			clusterName = cluster.Name
			segment = clusterName.String()
		}

		baseURL, found := idx.BaseURL(ShardNameFrom(ctx))
		if !found {
			logger.WithValues("shard", ShardNameFrom(ctx)).V(4).Info("unknown shard")
			ForbiddenHandler().ServeHTTP(w, req)
			return
		}
		shardURL, err := url.Parse(baseURL)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		// If the cluster was recently migrated and the request is not a fresh list or watch
		// return a 410 to force the client to do a relist.
		//
		// This is only required for watches.
		// Lists with a lower RV will get appropriate objects from the shard.
		// Lists with a higher RV will get a 504 from the shard.
		if !clusterName.Empty() && idx.RecentlyMigrated(clusterName) && isInProgressWatch(req) {
			err := apierrors.NewResourceExpired(fmt.Sprintf("logical cluster %q was recently migrated to another shard; watches with an explicit resourceVersion are temporarily denied to force a relist against the new shard", clusterName))
			responsewriters.ErrorNegotiated(err, kubernetesscheme.Codecs, schema.GroupVersion{}, w, req)
			return
		}

		shardURL.Path = strings.TrimSuffix(shardURL.Path, "/") + "/clusters/" + segment + strings.TrimSuffix(req.URL.Path, "/")
		shardURL.RawQuery = req.URL.RawQuery
		logger.WithValues("cluster", segment, "to", shardURL).V(4).Info("Redirecting")

		ctx = WithShardURL(ctx, shardURL)

		// Bound watch requests by the per-cluster context to cancel them when the cluster is migrated or deleted.
		if !clusterName.Empty() && isWatchRequest(req) {
			var cancel context.CancelFunc
			ctx, cancel = idx.ClusterContext(ctx, clusterName)
			defer cancel()
		}

		delegate.ServeHTTP(w, req.WithContext(ctx))
	}
}

func isWatchRequest(req *http.Request) bool {
	if info, ok := request.RequestInfoFrom(req.Context()); ok && info.IsResourceRequest {
		return info.Verb == "watch"
	}
	switch req.URL.Query().Get("watch") {
	case "true", "1":
		return true
	}
	return false
}

func isInProgressWatch(req *http.Request) bool {
	rv := req.URL.Query().Get("resourceVersion")
	if rv == "" || rv == "0" {
		return false
	}

	return isWatchRequest(req)
}

type lookupKey int

const (
	shardContextKey lookupKey = iota
	shardNameContextKey
	shardNameHolderContextKey
	workspaceTypeContextKey
	mountContextKey
)

// Mount describes the target of a request for a mounted workspace.
type Mount struct {
	// URL is the validated mount target (spec.URL of the mounted Workspace).
	URL *url.URL
	// Workspace is the full path of the mounted workspace as requested.
	Workspace logicalcluster.Path
	// ParentCluster holds the Workspace object describing the mount.
	ParentCluster logicalcluster.Name
}

func WithShardURL(parent context.Context, shardURL *url.URL) context.Context {
	return context.WithValue(parent, shardContextKey, shardURL)
}

func ShardURLFrom(ctx context.Context) *url.URL {
	shardURL, ok := ctx.Value(shardContextKey).(*url.URL)
	if !ok {
		return nil
	}
	return shardURL
}

func WithShardName(parent context.Context, shardName string) context.Context {
	// Also update the holder if one exists, so outer middleware can access the shard name
	// even though they only have access to the original request's context.
	if holder, ok := parent.Value(shardNameHolderContextKey).(*ShardNameHolder); ok {
		holder.Name = shardName
	}
	return context.WithValue(parent, shardNameContextKey, shardName)
}

func ShardNameFrom(ctx context.Context) string {
	shardName, ok := ctx.Value(shardNameContextKey).(string)
	if !ok {
		return ""
	}
	return shardName
}

// ShardNameHolder is a mutable container for the shard name that can be stored
// in context before the shard is known, then updated later. This allows outer
// middleware to access the shard name even when inner handlers create new
// request objects with WithContext().
type ShardNameHolder struct {
	Name string
}

// WithShardNameHolder stores a ShardNameHolder in the context. The holder can
// be updated later when WithShardName is called.
func WithShardNameHolder(parent context.Context) (context.Context, *ShardNameHolder) {
	holder := &ShardNameHolder{}
	return context.WithValue(parent, shardNameHolderContextKey, holder), holder
}

func WithWorkspaceType(parent context.Context, wsType logicalcluster.Path) context.Context {
	return context.WithValue(parent, workspaceTypeContextKey, wsType)
}

func WorkspaceTypeFrom(ctx context.Context) logicalcluster.Path {
	cluster, ok := ctx.Value(workspaceTypeContextKey).(logicalcluster.Path)
	if !ok {
		return logicalcluster.None
	}
	return cluster
}

// WithMount stores the mount target of the request.
func WithMount(parent context.Context, mount *Mount) context.Context {
	return context.WithValue(parent, mountContextKey, mount)
}

// MountFrom returns the mount target, or nil if the request is not for a mounted workspace.
func MountFrom(ctx context.Context) *Mount {
	mount, _ := ctx.Value(mountContextKey).(*Mount)
	return mount
}
