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
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"gopkg.in/square/go-jose.v2/jwt"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"

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

// WithGlobalServiceAccountRewrite replaces the user info of the context for service
// accounts by representing them as globally valid kcp service account representation
// with a warrant in the old format (to match kube-like (cluster) role bindings).
func WithGlobalServiceAccountRewrite(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		info, exists := request.UserFrom(ctx)
		if !exists {
			responsewriters.InternalError(w, req, errors.New("no user found for request"))
			return
		}

		clusters := info.GetExtra()[serviceaccount.ClusterNameKey]
		if !strings.HasPrefix(info.GetName(), "system:serviceaccount:") || len(clusters) != 1 {
			// multiple clusters are handled by authorization, but they cannot be used
			// cross-cluster ¯\_(ツ)_/¯
			handler.ServeHTTP(w, req)
			return
		}

		// move traditional kube service account into a warrant
		war := validation.Warrant{
			User:   info.GetName(),
			Groups: info.GetGroups(),
			UID:    info.GetUID(),
			Extra:  info.GetExtra(),
		}
		bs, err := json.Marshal(war)
		if err != nil {
			responsewriters.InternalError(w, req, fmt.Errorf("failed to marshal warrant: %v", err))
			return
		}

		// wrap with globally valid kcp service account
		comps := strings.SplitN(info.GetName(), ":", 4)
		if len(comps) != 4 {
			responsewriters.InternalError(w, req, errors.New("invalid service account name"))
			return
		}
		ns, user := comps[2], comps[3]
		rewritten := authnuser.DefaultInfo{
			Name:   fmt.Sprintf("system:kcp:serviceaccount:%s:%s:%s", clusters[0], ns, user),
			Groups: []string{authnuser.AllAuthenticated},
			Extra:  map[string][]string{validation.WarrantExtraKey: {string(bs)}},
		}
		for k, v := range info.GetExtra() {
			switch k {
			case validation.ScopeExtraKey:
				rewritten.Extra[k] = v
			default:
			}
		}

		// call rest of the chain
		ctx = request.WithUser(ctx, &rewritten)
		handler.ServeHTTP(w, req.WithContext(ctx))
	})
}
