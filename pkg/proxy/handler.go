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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	kcpauthorization "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
)

func shardHandler(index index.Index, proxy http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var cs = strings.SplitN(strings.TrimLeft(req.URL.Path, "/"), "/", 3)
		if len(cs) != 3 || cs[0] != "clusters" {
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

		clusterName := logicalcluster.New(cs[1])
		if !clusterName.IsValid() {
			// this includes wildcards
			logger.WithValues("path", req.URL.Path).V(4).Info("Invalid cluster name")
			responsewriters.Forbidden(req.Context(), attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
			return
		}

		shardURLString, canonicalPath, found := index.LookupURL(clusterName)
		if !found {
			logger.WithValues("clusterName", clusterName).V(4).Info("Unknown cluster")
			responsewriters.Forbidden(req.Context(), attributes, w, req, kcpauthorization.WorkspaceAccessNotPermittedReason, kubernetesscheme.Codecs)
			return
		}
		shardURL, err := url.Parse(shardURLString)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		logger.WithValues("from", req.URL.Path, "to", shardURL).V(4).Info("Redirecting")

		ctx = tenancy.WithCanonicalPath(WithShardURL(ctx, shardURL), canonicalPath)
		req = req.WithContext(ctx)
		proxy.ServeHTTP(w, req)
	}
}
