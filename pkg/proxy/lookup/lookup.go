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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpauthorization "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/index"
	proxyindex "github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

// WithClusterResolver resolves /clusters/ requests to the owning shard.
// Other paths are passed to delegate unchanged.
func WithClusterResolver(delegate http.Handler, mappings []types.PathMapping, index proxyindex.Index) http.Handler {
	mux := http.NewServeMux()

	// fallback for all unrecognized URLs
	mux.Handle("/", delegate)

	for _, mapping := range mappings {
		// Even though we know how to handle the "special" core clusters path,
		// the mapping provides additional PKI configuration that is not available
		// by just looking up the cluster in the index and figuring out the
		// target shard. That's why it's required to configure /clusters/ in the
		// front-proxy mappings and since admins could choose not to include it,
		// we only enable the built-in clusterResolveHandler if we actually find
		// an appropriate mapping.
		if strings.TrimRight(mapping.Path, "/") == "/clusters" {
			resolveHandler := newClusterResolveHandler(delegate, index)
			mux.HandleFunc("/clusters/{cluster}", resolveHandler)
			mux.HandleFunc("/clusters/{cluster}/{trail...}", resolveHandler)
		}
	}

	return mux
}

func newClusterResolveHandler(delegate http.Handler, index proxyindex.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		clusterName := req.PathValue("cluster")

		req, result := resolveClusterName(w, req, index, clusterName)
		if req == nil {
			return
		}

		// If the cluster was recently migrated and the request is not a fresh list or watch
		// return a 410 to force the client to do a relist.
		//
		// This is only required for watches.
		// Lists with a lower RV will get appropriate objects from the shard.
		// Lists with a higher RV will get a 504 from the shard.
		if index.RecentlyMigrated(result.Cluster) && isInProgressWatch(req) {
			err := apierrors.NewResourceExpired(fmt.Sprintf("logical cluster %q was recently migrated to another shard; watches with an explicit resourceVersion are temporarily denied to force a relist against the new shard", result.Cluster))
			responsewriters.ErrorNegotiated(err, kubernetesscheme.Codecs, schema.GroupVersion{}, w, req)
			return
		}

		shardURL, err := url.Parse(result.URL)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		ctx := req.Context()

		logger := klog.FromContext(ctx)
		logger.WithValues("from", "/clusters/"+clusterName, "to", shardURL).V(4).Info("Redirecting")

		shardURL.RawQuery = req.URL.RawQuery
		shardURL.Path = strings.TrimSuffix(shardURL.Path, "/")
		if trail := req.PathValue("trail"); len(trail) != 0 {
			shardURL.Path += "/" + trail
		}

		ctx = WithShardURL(ctx, shardURL)

		// Bound watch requests by the per-cluster context to cancel them when the cluster is migrated or deleted.
		if isWatchRequest(req) {
			var cancel context.CancelFunc
			ctx, cancel = index.ClusterContext(ctx, result.Cluster)
			defer cancel()
		}

		req = req.WithContext(ctx)

		delegate.ServeHTTP(w, req)
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

func resolveClusterName(w http.ResponseWriter, req *http.Request, index proxyindex.Index, clusterName string) (*http.Request, *index.Result) {
	ctx := req.Context()
	logger := klog.FromContext(ctx)
	attributes, err := filters.GetAuthorizerAttributes(ctx)
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return nil, nil
	}

	clusterPath := logicalcluster.NewPath(clusterName)
	if !clusterPath.IsValid() {
		// this includes wildcards
		logger.WithValues("requestPath", req.URL.Path).V(4).Info("Invalid cluster path")
		responsewriters.Forbidden(attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
		return nil, nil
	}

	result, found := index.LookupURL(clusterPath)
	if result.ErrorCode != 0 {
		http.Error(w, "Not available.", result.ErrorCode)
		return nil, nil
	}
	if !found {
		logger.WithValues("clusterPath", clusterPath).V(4).Info("Unknown cluster path")
		responsewriters.Forbidden(attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
		return nil, nil
	}

	ctx = WithShardName(ctx, result.Shard)

	return req.WithContext(ctx), &result
}

type lookupKey int

const (
	shardContextKey lookupKey = iota
	shardNameContextKey
	shardNameHolderContextKey
)

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
