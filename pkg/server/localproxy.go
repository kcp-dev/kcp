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
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
	"github.com/kcp-dev/kcp/pkg/server/filters"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
)

// WithLocalProxy returns a handler with a local-only mini-front-proxy. It is
// able to translate logical clusters with the data on the local shard. This is
// mainly interesting for standalone mode, without a real front-proxy in-front.
func WithLocalProxy(
	handler http.Handler,
	shardName, shardBaseURL, additionalMappingsFile string,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) (http.Handler, error) {
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

	defaultHandlerFunc := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger := klog.FromContext(ctx)

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
		r, found := indexState.Lookup(path)
		if found && r.ErrorCode != 0 {
			// return code if set.
			// TODO(mjudeikis): Would be nice to have a way to return a custom error message.
			http.Error(w, "Not available.", r.ErrorCode)
			return
		}
		if found && r.Shard != shardName && r.URL == "" {
			logger.WithValues("cluster", cluster.Name, "requestedShard", r.Shard, "actualShard", shardName).Info("cluster is not on this shard, but on another")

			w.Header().Set("Retry-After", fmt.Sprintf("%d", 1))
			http.Error(w, "Not found on this shard", http.StatusTooManyRequests)
			return
		}

		if r.URL != "" {
			u, err := url.Parse(r.URL)
			if err != nil {
				logger.WithValues("cluster", cluster.Name, "url", r.URL).Error(err, "invalid url")
				http.Error(w, "invalid url", http.StatusInternalServerError)
				return
			}
			logger.WithValues("from", path, "to", r.URL).V(4).Info("mounting cluster")
			proxy := httputil.NewSingleHostReverseProxy(u)
			// TODO(mjudeikis): remove this once we have a real cert wired in dev mode
			proxy.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			}

			proxy.ServeHTTP(w, req)
			return
		}

		// if this is a non-path request, there is nothing to lookup
		clusterName, isName := path.Name()

		if !isName && !found {
			// No rewrite, depend on the handler chain to do the right thing, like 403 or 404.
			cluster.Name = logicalcluster.Name(path.String())
			logger.WithValues("cluster", cluster.Name).Info("cluster not found")
			handler.ServeHTTP(w, req.WithContext(request.WithCluster(ctx, cluster)))
			return
		}

		if found {
			if r.Cluster.Path() != path {
				logger.WithValues("from", path, "to", r.Cluster).V(4).Info("rewriting cluster")
			}
			cluster.Name = r.Cluster
		} else {
			// we check "!isName && !foundInIndex" above. So, here it is a name.
			cluster.Name = clusterName
		}
		ctx = request.WithCluster(ctx, cluster)

		handler.ServeHTTP(w, req.WithContext(ctx))
	})

	// If additional mappings file is provided, read it and add the mappings to the handler
	handlers, err := NewLocalProxyHandler(defaultHandlerFunc, indexState, additionalMappingsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create local proxy handler: %w", err)
	}

	return handlers, nil
}

// NewLocalProxyHandler returns a handler with a local-only mini-front-proxy.
// This function is very similar to proxy/mapping.go.NewHandler.
// If we want to re-use that code, we basically would be merging proxy with server packages.
// Which is not desirable at the point of writing (2024-10-26), but might be in the future.
func NewLocalProxyHandler(defaultHandler http.Handler, index index.Index, additionalMappingsFile string) (http.Handler, error) {
	mapping := []proxy.PathMapping{}
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
		Index: index,
		Mappings: proxy.HttpHandlerMappings{
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

		handlers.Mappings = append(handlers.Mappings, proxy.HttpHandlerMapping{
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
			appendClientCertAuthHeaders(r.Header, u, userHeader, groupHeader, extraHeaderPrefix)
		}

		delegate.ServeHTTP(w, r)
	}
}

func appendClientCertAuthHeaders(header http.Header, user userinfo.Info, userHeader, groupHeader, extraHeaderPrefix string) {
	header.Set(userHeader, user.GetName())

	for _, group := range user.GetGroups() {
		header.Add(groupHeader, group)
	}

	for k, values := range user.GetExtra() {
		// Key must be encoded to enable e.g authentication.kubernetes.io/cluster-name
		// This is decoded in the RequestHeader auth handler
		encodedKey := url.PathEscape(k)
		for _, v := range values {
			header.Add(extraHeaderPrefix+encodedKey, v)
		}
	}
}
