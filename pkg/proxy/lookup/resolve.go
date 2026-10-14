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

package lookup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	kcpauthorization "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/mounts"
)

const clustersPrefix = "/clusters/"

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)

	workspacesResource = schema.GroupResource{
		Group:    tenancyv1alpha1.SchemeGroupVersion.Group,
		Resource: "workspaces",
	}
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

// WithClusterResolution resolves the /clusters/<segment> prefix of the
// request URL against idx, strips it from the URL and stores the result in
// the request context.
//
// Requests without a /clusters/ prefix pass through unchanged.
func WithClusterResolution(handler http.Handler, idx index.Index) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.URL.Path, clustersPrefix) {
			handler.ServeHTTP(w, req)
			return
		}
		segment, rest, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, clustersPrefix), "/")

		ctx, ok := resolveCluster(w, req, idx, segment)
		if !ok {
			return
		}

		req = req.WithContext(ctx)
		u := *req.URL
		u.Path = "/" + rest
		u.RawPath = ""
		req.URL = &u

		handler.ServeHTTP(w, req)
	})
}

// WithClusterResolutionPattern resolves the cluster segment matched by
// the mux pattern wildcard named pathValue against idx and stores the
// result in the request context.
//
// The URL is left unchanged. This differs from
// WithClusterResolution since this is used e.g. for the mounted
// front-proxy services, where the path needs to be retained.
//
// Requests without a matched segment pass through unchanged.
func WithClusterResolutionPattern(handler http.Handler, idx index.Index, pathValue string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		segment := req.PathValue(pathValue)
		if segment == "" {
			handler.ServeHTTP(w, req)
			return
		}

		ctx, ok := resolveCluster(w, req, idx, segment)
		if !ok {
			return
		}

		handler.ServeHTTP(w, req.WithContext(ctx))
	})
}

// resolveCluster resolves a cluster segment against idx.
// Returns the request context extended with the result,
// and false if a response was written.
//
// A malformed segment is rejected with 400,
// a segment unknown to idx with 403.
func resolveCluster(w http.ResponseWriter, req *http.Request, idx index.Index, segment string) (context.Context, bool) {
	ctx := req.Context()
	logger := klog.FromContext(ctx)

	if segment == logicalcluster.Wildcard.String() {
		ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})
		return ctx, true
	}

	path := logicalcluster.NewPath(segment)
	if path.Empty() || !path.IsValid() {
		responsewriters.ErrorNegotiated(
			apierrors.NewBadRequest(fmt.Sprintf("invalid cluster: %q is not a recognize cluster notation", segment)),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		return nil, false
	}

	result, found := idx.Lookup(path)
	if !found {
		logger.WithValues("path", path).V(4).Info("unknown cluster")
		forbidden(w, req, path)
		return nil, false
	}
	if result.ErrorCode != 0 {
		http.Error(w, "Not available.", result.ErrorCode)
		return nil, false
	}

	switch {
	case result.IsMount():
		target, err := mounts.ValidateURL(result.URL)
		if err != nil {
			logger.WithValues("path", path, "url", result.URL).Error(err, "mount target URL is invalid")
			responsewriters.ErrorNegotiated(
				apierrors.NewServiceUnavailable(fmt.Sprintf("the mount target of workspace %q is invalid", path)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return nil, false
		}

		parentPath, _ := path.Split()
		parent, found := idx.Lookup(parentPath)
		if !found || parent.Cluster.Empty() {
			logger.WithValues("path", path, "parent", parentPath).V(4).Info("parent of mounted workspace not found")
			forbidden(w, req, path)
			return nil, false
		}

		ctx = WithMount(ctx, &Mount{
			URL:           target,
			Workspace:     path,
			ParentCluster: parent.Cluster,
		})
		ctx = WithShardName(ctx, result.Shard)

	default:
		if result.Cluster.Path() != path {
			logger.WithValues("from", path, "to", result.Cluster).V(4).Info("rewriting cluster")
		}
		ctx = request.WithCluster(ctx, request.Cluster{Name: result.Cluster})
		ctx = WithClusterName(ctx, result.Cluster)
		ctx = WithWorkspaceType(ctx, result.Type)
		ctx = WithShardName(ctx, result.Shard)
	}

	return ctx, true
}

func forbidden(w http.ResponseWriter, req *http.Request, path logicalcluster.Path) {
	responsewriters.ErrorNegotiated(
		apierrors.NewForbidden(workspacesResource, path.String(), errors.New(kcpauthorization.WorkspaceAccessNotPermittedReason)),
		errorCodecs, schema.GroupVersion{}, w, req,
	)
}
