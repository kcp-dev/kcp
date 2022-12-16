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

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	corev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/core/v1alpha1"
	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
	"github.com/kcp-dev/kcp/pkg/server/filters"
)

// WithLocalProxy returns a handler with a local-only mini-front-proxy. It is
// able to translate logical clusters with the data on the local shard. This is
// mainly interesting for standalone mode, without a real front-proxy in-front.
func WithLocalProxy(
	handler http.Handler,
	shardName, shardBaseURL string,
	workspaceInformer tenancyv1beta1informers.WorkspaceClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) http.Handler {
	indexState := index.New([]index.PathRewriter{
		indexrewriters.UserRewriter,
	})
	indexState.UpsertShard(shardName, shardBaseURL)

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws := obj.(*tenancyv1beta1.Workspace)
			indexState.UpsertWorkspace(shardName, ws)
		},
		UpdateFunc: func(old, obj interface{}) {
			ws := obj.(*tenancyv1beta1.Workspace)
			indexState.UpsertWorkspace(shardName, ws)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			ws := obj.(*tenancyv1beta1.Workspace)
			indexState.DeleteWorkspace(shardName, ws)
		},
	})

	logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			this := obj.(*corev1alpha1.LogicalCluster)
			indexState.UpsertLogicalCluster(shardName, this)
		},
		UpdateFunc: func(old, obj interface{}) {
			this := obj.(*corev1alpha1.LogicalCluster)
			indexState.UpsertLogicalCluster(shardName, this)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			this := obj.(*corev1alpha1.LogicalCluster)
			indexState.DeleteLogicalCluster(shardName, this)
		},
	})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		path, newURL, foundInIndex, err := filters.ClusterPathFromAndStrip(req)
		if err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest(err.Error()),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}
		if foundInIndex {
			req.URL = newURL
		}

		cluster := request.Cluster{
			PartialMetadataRequest: filters.IsPartialMetadataRequest(req.Context()),
			Wildcard:               path == logicalcluster.Wildcard,
		}

		if path.Empty() || cluster.Wildcard {
			ctx := request.WithCluster(req.Context(), cluster)
			handler.ServeHTTP(w, req.WithContext(ctx))
			return
		}

		if !path.IsValid() {
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest(fmt.Sprintf("invalid cluster: %q does not match the regex", path)),
				errorCodecs, schema.GroupVersion{},
				w, req)
			return
		}

		// here, we have a real path we have to translate to get a logical cluster name

		// first pure names
		if name, isName := path.Name(); isName {
			cluster.Name = name
			handler.ServeHTTP(w, req.WithContext(request.WithCluster(ctx, cluster)))
			return
		}

		// lookup in our local, potentially partial index
		requestShardName, rewrittenClusterName, foundInIndex := indexState.Lookup(path)
		if foundInIndex && requestShardName != shardName {
			klog.Infof("Cluster %q is not on this shard, but on %q", cluster.Name, requestShardName)

			w.Header().Set("Retry-After", fmt.Sprintf("%d", 1))
			http.Error(w, "Not found on this shard", http.StatusTooManyRequests)
			return
		}

		// if this is a non-path request, there is nothing to lookup
		clusterName, isName := path.Name()

		if !isName && !foundInIndex {
			// No rewrite, depend on the handler chain to do the right thing, like 403 or 404.
			cluster.Name = logicalcluster.Name(path.String())
			klog.Infof("Cluster %q not found", cluster.Name)
			handler.ServeHTTP(w, req.WithContext(request.WithCluster(ctx, cluster)))
			return
		}

		if foundInIndex {
			if rewrittenClusterName.Path() != path {
				klog.FromContext(ctx).V(4).Info("Rewriting cluster", "from", path, "to", rewrittenClusterName)
			}
			cluster.Name = rewrittenClusterName
		} else {
			// we check "!isName && !foundInIndex" above. So, here it is a name.
			cluster.Name = clusterName
		}
		ctx = request.WithCluster(ctx, cluster)

		handler.ServeHTTP(w, req.WithContext(ctx))
	})
}
