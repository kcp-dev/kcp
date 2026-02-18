/*
Copyright 2025 The KCP Authors.

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
	"net/http"
	"net/url"
	"strings"

	"k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpauthorization "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/index"
	proxyindex "github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

func WithClusterResolver(delegate http.Handler, mappings []types.PathMapping, index proxyindex.Index) http.Handler {
	mux := http.NewServeMux()

	// fallback for all unrecognized URLs
	mux.Handle("/", delegate)

	// Use the extra path mappings as an additional source of cluster names in URLs;
	// it's okay for a virtual workspace URL to not match here or to not have a
	// cluster placeholder in its URL pattern, since the default handler will simply
	// forward the request unchanged (and most likely, unauthenticated).

	// We can use the same handler for all mappings, since the actual muxing to
	// the destinations happens later in proxy.HttpHandler; here we only care about
	// detecting the cluster name.
	mappingHandler := newMappingHandler(delegate, index)

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
			// we know how to parse cluster URLs
			resolveHandler := newClusterResolveHandler(delegate, index)
			mux.HandleFunc("/clusters/{cluster}", resolveHandler)
			mux.HandleFunc("/clusters/{cluster}/{trail...}", resolveHandler)
		} else {
			// mappings are configured with *prefixes*; in order to match both exact matches
			// and prefix matches (i.e. if "/foo" is configured, both "/foo" and "/foo/bar"
			// must match), each mapping is added twice to the mux.
			mux.HandleFunc(p, mappingHandler)
			mux.HandleFunc(p+"/{trail...}", mappingHandler)
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
		req = req.WithContext(ctx)

		delegate.ServeHTTP(w, req)
	}
}

func newMappingHandler(delegate http.Handler, index proxyindex.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// not every virtual workspace and/or every mapping has a {cluster} in its URL;
		// also wildcard requests have to be passed without lookup
		clusterName := req.PathValue("cluster")
		if clusterName == "" || clusterName == "*" {
			delegate.ServeHTTP(w, req)
			return
		}

		req, _ = resolveClusterName(w, req, index, clusterName)
		if req == nil {
			return
		}

		delegate.ServeHTTP(w, req)
	}
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

	ctx = WithClusterName(ctx, result.Cluster)
	ctx = WithWorkspaceType(ctx, result.Type)

	return req.WithContext(ctx), &result
}

type lookupKey int

const (
	shardContextKey lookupKey = iota
	clusterContextKey
	workspaceTypeContextKey
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

func WithClusterName(parent context.Context, cluster logicalcluster.Name) context.Context {
	return context.WithValue(parent, clusterContextKey, cluster)
}

func ClusterNameFrom(ctx context.Context) logicalcluster.Name {
	cluster, ok := ctx.Value(clusterContextKey).(logicalcluster.Name)
	if !ok {
		return ""
	}
	return cluster
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
