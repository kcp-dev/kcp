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

package filters

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/munnerz/goautoneg"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
)

type (
	acceptHeaderContextKeyType int
)

const (
	workspaceAnnotation = "tenancy.kcp.dev/workspace"

	// clusterKey is the context key for the request namespace.
	acceptHeaderContextKey acceptHeaderContextKeyType = iota
)

var (
	// reClusterName is a regular expression for cluster names. It is based on
	// modified RFC 1123. It allows for 63 characters for single name and includes
	// KCP specific ':' separator for workspace nesting. We are not re-using k8s
	// validation regex because its purpose is for single name validation
	reClusterName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?:)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

// WithAuditEventClusterAnnotation adds the cluster name into the annotation of an audit
// event. Needs initialized annotations.
func WithAuditEventClusterAnnotation(handler http.Handler) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cluster := request.ClusterFrom(req.Context())
		if cluster != nil {
			kaudit.AddAuditAnnotation(req.Context(), workspaceAnnotation, cluster.Name.String())
		}

		handler.ServeHTTP(w, req)
	})
}

// WithClusterScope reads a cluster name from the URL path and puts it into the context.
// It also trims "/clusters/" prefix from the URL.
func WithClusterScope(apiHandler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var clusterName logicalcluster.Name
		if path := req.URL.Path; strings.HasPrefix(path, "/clusters/") {
			path = strings.TrimPrefix(path, "/clusters/")

			i := strings.Index(path, "/")
			if i == -1 {
				path = path + "/"
				i = len(path) - 1
			}
			clusterName, path = logicalcluster.New(path[:i]), path[i:]
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

		var cluster request.Cluster

		// This is necessary so wildcard (cross-cluster) partial metadata requests can succeed. The storage layer needs
		// to know if a request is for partial metadata to be able to extract the cluster name from storage keys
		// properly.
		cluster.PartialMetadataRequest = IsPartialMetadataRequest(req.Context())

		switch {
		case clusterName == logicalcluster.Wildcard:
			// HACK: just a workaround for testing
			cluster.Wildcard = true
			// fallthrough
			cluster.Name = logicalcluster.Wildcard
		case !clusterName.Empty():
			if !reClusterName.MatchString(clusterName.String()) {
				responsewriters.ErrorNegotiated(
					apierrors.NewBadRequest(fmt.Sprintf("invalid cluster: %q does not match the regex", clusterName)),
					errorCodecs, schema.GroupVersion{},
					w, req)
				return
			}
			cluster.Name = clusterName
		}

		ctx := request.WithCluster(req.Context(), cluster)
		apiHandler.ServeHTTP(w, req.WithContext(ctx))
	}
}

// WithAcceptHeader makes the Accept header available for code in the handler chain. It is needed for
// Wildcard requests, when finding the CRD with a common schema. For PartialObjectMeta requests we cand
// weaken the schema requirement and allow different schemas across workspaces.
func WithAcceptHeader(apiHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), acceptHeaderContextKey, req.Header.Get("Accept"))
		apiHandler.ServeHTTP(w, req.WithContext(ctx))
	})
}

// IsPartialMetadataRequest determines if it is PartialObjectMetadata request
// based on the value stored in the context.
//
// A PartialObjectMetadata request gets only object metadata.
func IsPartialMetadataRequest(ctx context.Context) bool {
	accept, ok := ctx.Value(acceptHeaderContextKey).(string)
	if !ok || accept == "" {
		return false
	}

	return isPartialMetadataHeader(accept)
}

func isPartialMetadataHeader(accept string) bool {
	clauses := goautoneg.ParseAccept(accept)
	for _, clause := range clauses {
		if clause.Params["as"] == "PartialObjectMetadata" || clause.Params["as"] == "PartialObjectMetadataList" {
			return true
		}
	}

	return false
}
