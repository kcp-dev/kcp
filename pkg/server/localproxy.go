/*
Copyright 2021 The kcp Authors.

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
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"sigs.k8s.io/yaml"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authentication"
	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
	"github.com/kcp-dev/kcp/pkg/proxy/authheaders"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
	"github.com/kcp-dev/kcp/pkg/server/filters"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

func newLocalClusterIndex(
	shardName, shardBaseURL string,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) *index.State {
	indexState := index.New([]index.PathRewriter{
		indexrewriters.UserRewriter,
	})
	indexState.UpsertShard(shardName, shardBaseURL)

	_, _ = workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws := obj.(*tenancyv1alpha1.Workspace)
			indexState.UpsertWorkspace(shardName, ws)
		},
		UpdateFunc: func(old, obj interface{}) {
			ws := obj.(*tenancyv1alpha1.Workspace)
			indexState.UpsertWorkspace(shardName, ws)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			ws := obj.(*tenancyv1alpha1.Workspace)
			indexState.DeleteWorkspace(shardName, ws)
		},
	})

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			logicalCluster := obj.(*corev1alpha1.LogicalCluster)
			indexState.UpsertLogicalCluster(shardName, logicalCluster)
		},
		UpdateFunc: func(old, obj interface{}) {
			logicalCluster := obj.(*corev1alpha1.LogicalCluster)
			indexState.UpsertLogicalCluster(shardName, logicalCluster)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			logicalCluster := obj.(*corev1alpha1.LogicalCluster)
			indexState.DeleteLogicalCluster(shardName, logicalCluster)
		},
	})

	return indexState
}

// WithLocalProxy returns a handler with a local-only mini-front-proxy.
// It acts on the cluster resolved by lookup.WithClusterResolution.
// Requests for mounted workspaces are checked and continue into the chain
// for WithMountProxy to forward after authentication.
// Requests for clusters not on the shard are rejected.
func WithLocalProxy(
	handler http.Handler,
	shardName, additionalMappingsFile string,
) (http.Handler, error) {
	defaultHandlerFunc := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger := klog.FromContext(ctx)

		if mount := lookup.MountFrom(ctx); mount != nil {
			// Impersonation needs a logical cluster to authorize against, which a
			// mount does not have. Reject it explicitly instead of letting the
			// impersonation filters fail in less obvious ways.
			if authentication.HasImpersonationHeaders(req.Header) {
				responsewriters.ErrorNegotiated(
					apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("impersonation is not supported for mounted workspaces")),
					errorCodecs, schema.GroupVersion{}, w, req,
				)
				return
			}

			logger.WithValues("from", mount.Workspace, "to", mount.URL.Scheme+"://"+mount.URL.Host).V(4).Info("resolved mounted workspace")
			handler.ServeHTTP(w, req)
			return
		}

		cluster := request.ClusterFrom(ctx)
		if cluster == nil {
			// Not a /clusters/ request.
			cluster = &request.Cluster{}
		}

		if shard := lookup.ShardNameFrom(ctx); shard != "" && shard != shardName {
			logger.WithValues("cluster", cluster.Name, "requestedShard", shard, "actualShard", shardName).Info("cluster is not on this shard, but on another")

			w.Header().Set("Retry-After", fmt.Sprintf("%d", 1))
			http.Error(w, "Not found on this shard", http.StatusTooManyRequests)
			return
		}

		// The storage layer needs to know if a request is for partial metadata
		// to extract the cluster name from storage keys of wildcard requests.
		cluster.PartialMetadataRequest = filters.IsPartialMetadataRequest(ctx)
		handler.ServeHTTP(w, req.WithContext(request.WithCluster(ctx, *cluster)))
	})

	// if no additional mappings are provided, do not start local-only mini-front-proxy.
	if additionalMappingsFile == "" {
		return defaultHandlerFunc, nil
	}

	// If additional mappings file is provided, read it and add the mappings to the handler
	handlers, err := NewLocalProxyHandler(defaultHandlerFunc, additionalMappingsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create local proxy handler: %w", err)
	}

	return handlers, nil
}

// NewLocalProxyHandler returns a handler with a local-only mini-front-proxy.
// This function is very similar to proxy/mapping.go.NewHandler.
// If we want to re-use that code, we basically would be merging proxy with server packages.
// Which is not desirable at the point of writing (2024-10-26), but might be in the future.
func NewLocalProxyHandler(defaultHandler http.Handler, additionalMappingsFile string) (http.Handler, error) {
	mapping := []types.PathMapping{}
	if additionalMappingsFile != "" {
		mappingData, err := os.ReadFile(additionalMappingsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read mapping file %q: %w", additionalMappingsFile, err)
		}

		if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
			return nil, fmt.Errorf("failed to unmarshal mapping file %q: %w", additionalMappingsFile, err)
		}
	}

	handlers := proxy.HttpHandler{
		Mappings: types.HttpHandlerMappings{
			{
				Weight:  0,
				Path:    "/metrics",
				Handler: legacyregistry.Handler(),
			},
		},
		DefaultHandler: defaultHandler,
	}

	for _, m := range mapping {
		u, err := url.Parse(m.Backend)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: failed to parse URL %q: %w", m.Path, m.Backend, err)
		}

		// This is insecure, but just don't tell it to anyone :)
		transport, err := newInsecureTransport()
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: %w", m.Path, err)
		}

		var handler http.Handler
		p := httputil.NewSingleHostReverseProxy(u)
		p.Transport = transport
		handler = p

		userHeader := "X-Remote-User"
		groupHeader := "X-Remote-Group"
		extraHeaderPrefix := "X-Remote-Extra-"
		if m.UserHeader != "" {
			userHeader = m.UserHeader
		}
		if m.GroupHeader != "" {
			groupHeader = m.GroupHeader
		}
		if m.ExtraHeaderPrefix != "" {
			extraHeaderPrefix = m.ExtraHeaderPrefix
		}

		handler = withProxyAuthHeaders(handler, userHeader, groupHeader, extraHeaderPrefix)

		handlers.Mappings = append(handlers.Mappings, types.HttpHandlerMapping{
			Weight:  len(m.Path),
			Path:    m.Path,
			Handler: handler,
		})
	}

	handlers.Mappings.Sort()

	return &handlers, nil
}

func newInsecureTransport() (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	return transport, nil
}

// withProxyAuthHeaders does client cert termination by extracting the user and groups and
// passing them through access headers to the shard.
func withProxyAuthHeaders(delegate http.Handler, userHeader, groupHeader string, extraHeaderPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := request.UserFrom(r.Context()); ok {
			authheaders.SetAuthHeaders(r.Header, u, userHeader, groupHeader, extraHeaderPrefix)
		} else {
			authheaders.ClearAuthHeaders(r.Header, userHeader, groupHeader, extraHeaderPrefix)
		}

		delegate.ServeHTTP(w, r)
	}
}
