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

package filters

import (
	"net/http"
	"path"
	"strings"

	"gopkg.in/square/go-jose.v2/jwt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// WithInClusterServiceAccountRequestRewrite adds the /clusters/<clusterName> prefix to the request path if the request comes
// from an InCluster service account requests (InCluster clients don't support prefixes).
func WithInClusterServiceAccountRequestRewrite(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// some header we set for sharding, those are not the requests from InCluster clients
		shardedHeader := req.Header.Get("X-Kubernetes-Sharded-Request")

		if shardedHeader != "" {
			handler.ServeHTTP(w, req)
			return
		}

		if strings.HasPrefix(req.RequestURI, "/clusters/") {
			handler.ServeHTTP(w, req)
			return
		}

		if strings.HasPrefix(req.RequestURI, "/services/") {
			handler.ServeHTTP(w, req)
			return
		}

		prefix := "Bearer "
		token := req.Header.Get("Authorization")
		if !strings.HasPrefix(token, prefix) {
			handler.ServeHTTP(w, req)
			return
		}
		token = token[len(prefix):]

		var claims map[string]interface{}
		decoded, err := jwt.ParseSigned(token)
		if err != nil { // just ignore
			handler.ServeHTTP(w, req)
			return
		}
		if err = decoded.UnsafeClaimsWithoutVerification(&claims); err != nil {
			handler.ServeHTTP(w, req)
			return
		}

		clusterName, ok, err := unstructured.NestedString(claims, "kubernetes.io", "clusterName") // bound
		if err != nil || !ok {
			clusterName, ok, err = unstructured.NestedString(claims, "kubernetes.io/serviceaccount/clusterName") // legacy
			if err != nil || !ok {
				handler.ServeHTTP(w, req)
				return
			}
		}

		req.URL.Path = path.Join("/clusters", clusterName, req.URL.Path)
		req.RequestURI = path.Join("/clusters", clusterName, req.RequestURI)

		handler.ServeHTTP(w, req)
	})
}
