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
		loader.RLock()
		clients := loader.Clients
		loader.RUnlock()
		handler := apiserver.NewShardedHandler(clients, 0, 10*time.Minute)
		handler.ServeHTTP(w, req)
		return
	}
}
