/*
Copyright 2022 The kcp Authors.

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

package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/authorization/shardpaths"
)

var (
	shardNameRegExp = regexp.MustCompile(`^[a-z0-9-:]{0,61}$`)

	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

// WithShardScope reads a shard name from the URL path and puts it into the context.
// It also trims "/shards/" prefix from the URL.
// If the path doesn't contain the shard name then a 404 error is returned.
//
// For example:
//
// /shards/*/clusters/*/apis/apis.kcp.io/v1alpha1/apiexports
//
// /shards/amber/clusters/*/apis/apis.kcp.io/v1alpha1/apiexports
//
// /shards/sapphire/clusters/system:sapphire/apis/apis.kcp.io/v1alpha1/apiexports
//
// /shards/amber/clusters/system:amber/apis/apis.kcp.io/v1alpha1/apiexports
//
// Note:
// not all paths require to have a valid shard name. Any path declared
// shard-level in pkg/authorization/shardpaths (e.g. /metrics and the standard
// /livez, /readyz, /healthz probes) passes through so that prometheus-style
// scrapers and liveness probes can hit the cache server directly without
// constructing a /shards/<sh>/ prefix.
func WithShardScope(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if shardpaths.Paths.Has(req.URL.Path) {
			handler.ServeHTTP(w, req)
			return
		}
		var shardName string
		if path := req.URL.Path; strings.HasPrefix(path, "/shards/") {
			path = strings.TrimPrefix(path, "/shards/")

			i := strings.Index(path, "/")
			if i == -1 {
				responsewriters.ErrorNegotiated(
					apierrors.NewBadRequest(fmt.Sprintf("unable to parse shard: no `/` found in path %s", path)),
					errorCodecs, schema.GroupVersion{},
					w, req)
				return
			}
			shardName, path = path[:i], path[i:]
			req.URL.Path = path
			newURL, err := url.Parse(req.URL.String())
			if err != nil {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(fmt.Errorf("unable to resolve %s, err %w", req.URL.Path, err)),
					errorCodecs, schema.GroupVersion{},
					w, req)
				return
			}
			req.URL = newURL
		}

		var shard request.Shard
		switch {
		case shardName == "*":
			shard = "*"
		case len(shardName) == 0:
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest("a shard name is required"),
				errorCodecs, schema.GroupVersion{},
				w, req)
			return
		default:
			if !shardNameRegExp.MatchString(shardName) {
				responsewriters.ErrorNegotiated(
					apierrors.NewBadRequest(fmt.Sprintf("invalid shard: %q does not match the regex", shardName)),
					errorCodecs, schema.GroupVersion{},
					w, req)
				return
			}
			shard = request.Shard(shardName)
		}

		ctx := request.WithShard(req.Context(), shard)
		handler.ServeHTTP(w, req.WithContext(ctx))
	})
}

// WithServiceScope an HTTP filter that trims "/services/cache" prefix from the URL.
//
// for example: /services/cache/shards/amber/clusters/*/apis/apis.kcp.io/v1alpha1/apiexports
// is truncated to /shards/amber/clusters/*/apis/apis.kcp.io/v1alpha1/apiexports.
func WithServiceScope(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if path := req.URL.Path; strings.HasPrefix(path, "/services/cache") {
			path = strings.TrimPrefix(path, "/services/cache")
			req.URL.Path = path
			newURL, err := url.Parse(req.URL.String())
			if err != nil {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(fmt.Errorf("unable to resolve %s, err %w", req.URL.Path, err)),
					errorCodecs, schema.GroupVersion{},
					w, req)
				return
			}
			req.URL = newURL
		}
		handler.ServeHTTP(w, req)
	})
}

// WithCacheShardLevelPaths enforces that shard-level URLs (see
// pkg/authorization/shardpaths) are not reachable via a shard- or
// workspace-scoped cache server URL such as
// /services/cache/shards/<sh>/clusters/<ws>/metrics. The data exposed at these
// paths is process-wide and has no per-shard or per-workspace meaning.
//
// Must run AFTER WithClusterScope and WithShardScope so the request context
// reflects whether either prefix was present in the original URL.
func WithCacheShardLevelPaths(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !shardpaths.Paths.Has(req.URL.Path) {
			handler.ServeHTTP(w, req)
			return
		}
		shardScope := request.ShardFrom(req.Context())
		cluster := request.ClusterFrom(req.Context())
		if !shardScope.Empty() || (cluster != nil && !cluster.Name.Empty()) {
			audit.AddAuditAnnotation(req.Context(), "shardpaths.kcp.io/rejected", req.URL.Path)
			http.Error(w, "shard-level endpoint not available at shard or workspace scope", http.StatusNotImplemented)
			return
		}
		handler.ServeHTTP(w, req)
	})
}

// WithSyntheticDelay injects a synthetic delay to calls, to exacerbate timing issues and expose inconsistent client behavior.
func WithSyntheticDelay(handler http.Handler, delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(delay)
		handler.ServeHTTP(w, req)
	})
}
