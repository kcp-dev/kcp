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

package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
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
// If the path doesn't contain the shard name then a default "system:cache:server" name is assigned.
//
// For example:
//
// /shards/*/clusters/*/apis/apis.kcp.dev/v1alpha1/apiexports
//
// /shards/amber/clusters/*/apis/apis.kcp.dev/v1alpha1/apiexports
//
// /shards/sapphire/clusters/system:sapphire/apis/apis.kcp.dev/v1alpha1/apiexports
//
// /shards/amber/clusters/system:amber/apis/apis.kcp.dev/v1alpha1/apiexports
func WithShardScope(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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
			// because we don't store a shard name in an object.
			// requests without a shard name won't be able to find associated data and will fail.
			// as of today we don't instruct controllers used by the apiextention server
			// how to assign/extract a shard name to/from an object.
			// so we need to set a default name here, otherwise these controllers will fail.
			shard = "system:cache:server"
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
