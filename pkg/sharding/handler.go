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

package sharding

import (
	"net/http"
	"time"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/sharding/apiserver"
)

func ServeHTTP(apiHandler http.Handler, loader *ClientLoader) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		info, ok := request.RequestInfoFrom(req.Context())
		if !ok {
			http.Error(w, "no request info", http.StatusInternalServerError)
			return
		}
		if info == nil || !info.IsResourceRequest {
			// not a resource request, can't handle it
			apiHandler.ServeHTTP(w, req)
			return
		}
		if sharded := req.Header.Get("X-Kubernetes-Sharded-Request"); sharded == "true" {
			// we're being asked for our own data by some other shard, no need to fan out
			apiHandler.ServeHTTP(w, req)
			return
		}
		handler := apiserver.NewShardedHandler(loader.Clients(), 0, 10*time.Minute)
		handler.ServeHTTP(w, req)
	}
}
