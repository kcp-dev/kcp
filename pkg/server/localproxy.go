/*
Copyright 2021 The KCP Authors.

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

	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
)

// WithLocalProxy returns a handler with a local-only mini-front-proxy. It is
// able to translate logical clusters with the data on the local shard. This is
// mainly interesting for standalone mode, without a real front-proxy in-front.
func WithLocalProxy(
	handler http.Handler,
	shardName, shardBaseURL string,
	workspaceInformer tenancyv1beta1informers.WorkspaceClusterInformer,
	thisWorkspaceInformer tenancyv1alpha1informers.ThisWorkspaceClusterInformer,
) http.Handler {
	state := index.New([]index.PathRewriter{
		indexrewriters.UserRewriter,
	})
	state.UpsertShard(shardName, shardBaseURL)

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws := obj.(*tenancyv1beta1.Workspace)
			state.UpsertWorkspace(shardName, ws)
		},
		UpdateFunc: func(old, obj interface{}) {
			ws := obj.(*tenancyv1beta1.Workspace)
			state.UpsertWorkspace(shardName, ws)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			ws := obj.(*tenancyv1beta1.Workspace)
			state.DeleteWorkspace(shardName, ws)
		},
	})

	thisWorkspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			this := obj.(*tenancyv1alpha1.ThisWorkspace)
			state.UpsertThisWorkspace(shardName, this)
		},
		UpdateFunc: func(old, obj interface{}) {
			this := obj.(*tenancyv1alpha1.ThisWorkspace)
			state.UpsertThisWorkspace(shardName, this)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			this := obj.(*tenancyv1alpha1.ThisWorkspace)
			state.DeleteThisWorkspace(shardName, this)
		},
	})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		clusterInfo := request.ClusterFrom(ctx)
		if clusterInfo == nil || clusterInfo.Wildcard || clusterInfo.Name.Empty() {
			// No cluster info, or wildcard cluster. No need to translate.
			handler.ServeHTTP(w, req)
			return
		}

		canonicalPath, foundCanonicalPath := tenancy.CanonicalPathFromHeader(req.Header)
		requestShardName, rewrittenClusterName, computedCanonicalPath, found := state.Lookup(clusterInfo.Name.Path())
		if !found {
			// No rewrite, depend on the handler chain to do the right thing, like 403 or 404.
			handler.ServeHTTP(w, req)
			return
		}

		if foundCanonicalPath {
			// note: the client can of course fake this header, but it's not a security issue
			// because it can equally just put the same value in the API objects. The canonical
			// path must only be used for defaulting, not for something security-critical.
			canonicalPath = computedCanonicalPath
		} else if clusterInfo.Name == tenancyv1alpha1.RootCluster {
			canonicalPath = clusterInfo.Name.Path()
		}
		req = req.WithContext(tenancy.WithCanonicalPath(ctx, canonicalPath))

		if requestShardName != shardName {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", 1))
			http.Error(w, "Not found on this shard", http.StatusTooManyRequests)
			return
		}

		if rewrittenClusterName == clusterInfo.Name {
			handler.ServeHTTP(w, req)
			return
		}

		klog.FromContext(ctx).V(4).Info("Rewriting cluster", "from", clusterInfo.Name, "to", rewrittenClusterName)
		clusterInfo.Name = rewrittenClusterName
		handler.ServeHTTP(w, req.WithContext(request.WithCluster(ctx, *clusterInfo)))
	})
}
