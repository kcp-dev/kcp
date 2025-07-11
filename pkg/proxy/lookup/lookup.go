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
	"fmt"
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
)

func WithClusterResolver(delegate http.Handler, index proxyindex.Index) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var cs = strings.SplitN(strings.TrimLeft(req.URL.Path, "/"), "/", 3)
		if len(cs) < 2 || cs[0] != "clusters" {
			delegate.ServeHTTP(w, req)
			return
		}

		ctx := req.Context()
		logger := klog.FromContext(ctx)
		attributes, err := filters.GetAuthorizerAttributes(ctx)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		clusterPath := logicalcluster.NewPath(cs[1])
		if !clusterPath.IsValid() {
			// this includes wildcards
			logger.WithValues("requestPath", req.URL.Path).V(4).Info("Invalid cluster path")
			responsewriters.Forbidden(req.Context(), attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
			return
		}

		result, found := index.LookupURL(clusterPath)
		if result.ErrorCode != 0 {
			http.Error(w, "Not available.", result.ErrorCode)
			return
		}
		if !found {
			logger.WithValues("clusterPath", clusterPath).V(4).Info("Unknown cluster path")
			responsewriters.Forbidden(req.Context(), attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
			return
		}
		shardURL, err := url.Parse(result.URL)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		logger.WithValues("from", "/clusters/"+cs[1], "to", shardURL).V(4).Info("Redirecting")

		shardURL.Path = strings.TrimSuffix(shardURL.Path, "/")
		if len(cs) == 3 {
			shardURL.Path += "/" + cs[2]
		}

		fmt.Printf("XRSTF: clusterPath=%v\n", clusterPath)
		fmt.Printf("XRSTF: result=%#v\n", result)

		ctx = WithShardURL(ctx, shardURL)
		ctx = WithClusterName(ctx, result.Cluster)
		ctx = WithWorkspaceType(ctx, result.Type)
		req = req.WithContext(ctx)

		delegate.ServeHTTP(w, req)
	})
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

func WithWorkspaceType(parent context.Context, wsType *index.WorkspaceType) context.Context {
	return context.WithValue(parent, workspaceTypeContextKey, wsType)
}

func WorkspaceTypeFrom(ctx context.Context) *index.WorkspaceType {
	cluster, ok := ctx.Value(workspaceTypeContextKey).(*index.WorkspaceType)
	if !ok {
		return nil
	}
	return cluster
}
