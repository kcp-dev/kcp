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

package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	kcpauthorization "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
)

func shardHandler(index index.Index, proxy http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var cs = strings.SplitN(strings.TrimLeft(req.URL.Path, "/"), "/", 3)
		if len(cs) < 2 || cs[0] != "clusters" {
			http.NotFound(w, req)
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

		shardURLString, found := index.LookupURL(clusterPath)
		if !found {
			logger.WithValues("clusterPath", clusterPath).V(4).Info("Unknown cluster path")
			responsewriters.Forbidden(req.Context(), attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
			return
		}
		shardURL, err := url.Parse(shardURLString)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		logger.WithValues("from", "/clusters/"+cs[1], "to", shardURL).V(4).Info("Redirecting")

		shardURL.Path = strings.TrimSuffix(shardURL.Path, "/")
		if len(cs) == 3 {
			shardURL.Path += "/" + cs[2]
		}

		ctx = WithShardURL(ctx, shardURL)
		req = req.WithContext(ctx)
		proxy.ServeHTTP(w, req)
	}
}
