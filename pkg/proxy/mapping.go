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
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
	"github.com/kcp-dev/logicalcluster/v3"
)

// PathMapping describes how to route traffic from a path to a backend server.
// Each Path is registered with the DefaultServeMux with a handler that
// delegates to the specified backend.
type PathMapping struct {
	Path              string `json:"path"`
	Backend           string `json:"backend"`
	BackendServerCA   string `json:"backend_server_ca"`
	ProxyClientCert   string `json:"proxy_client_cert"`
	ProxyClientKey    string `json:"proxy_client_key"`
	UserHeader        string `json:"user_header,omitempty"`
	GroupHeader       string `json:"group_header,omitempty"`
	ExtraHeaderPrefix string `json:"extra_header_prefix"`
}

type HttpHandler struct {
	index          index.Index
	mapping        []httpHandlerMapping
	defaultHandler http.Handler
}

// httpHandlerMapping is used to route traffic to the correct backend server.
// Higher weight means that the mapping is more specific and should be matched first
type httpHandlerMapping struct {
	weight  int
	path    string
	handler http.Handler
}

func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// mappings are used to route traffic to the correct backend server.
	// It should not have `/clusters` as prefix because that is handled by the
	// shardHandler or mounts. Logic is as follows:
	// 1. We detect URL for the request and find the correct handler. URL can be
	// shard based, virtual workspace or mount. First two are covered by r.URL,
	// where mounts are covered by annotation on the workspace with the mount path.
	// 2. If mountpoint is found, we rewrite the URL to resolve, else use one in
	// request to match with mappings.
	// 3. Iterate over mappings and find the one that matches the URL. If found,
	// use the handler for that mapping, else use default handler - kcp.
	// Mappings are done from most specific to least specific:
	// Example: /clusters/cluster1/ will be matched before /clusters/
	for _, m := range h.mapping {
		if strings.HasPrefix(h.resolveURL(r), m.path) {
			m.handler.ServeHTTP(w, r)
			return
		}
	}

	h.defaultHandler.ServeHTTP(w, r)
}

func (h *HttpHandler) resolveURL(r *http.Request) string {
	// if we don't match any of the paths, use the default behavior - request
	var cs = strings.SplitN(strings.TrimLeft(r.URL.Path, "/"), "/", 3)
	if len(cs) < 2 || cs[0] != "clusters" {
		return r.URL.Path
	}
	clusterPath := logicalcluster.NewPath(cs[1])
	if !clusterPath.IsValid() {
		return r.URL.Path
	}

	u, found := h.index.LookupURL(clusterPath)
	if found {
		u, err := url.Parse(u)
		if err == nil && u != nil {
			u.Path = strings.TrimSuffix(u.Path, "/")
			r.URL.Path = path.Join(u.Path, strings.Join(cs[2:], "/")) // override request prefix and keep kube api contextual suffix
			return u.Path
		}
	}

	return r.URL.Path
}

func NewHandler(ctx context.Context, o *proxyoptions.Options, index index.Index) (http.Handler, error) {
	mappingData, err := os.ReadFile(o.MappingFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read mapping file %q: %w", o.MappingFile, err)
	}

	var mapping []PathMapping
	if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mapping file %q: %w", o.MappingFile, err)
	}

	handlers := HttpHandler{
		index: index,
		mapping: []httpHandlerMapping{
			{
				weight:  0,
				path:    "/metrics",
				handler: legacyregistry.Handler(),
			},
		},
	}

	logger := klog.FromContext(ctx)
	for _, m := range mapping {
		logger.WithValues("mapping", m).V(2).Info("adding mapping")

		u, err := url.Parse(m.Backend)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: failed to parse URL %q: %w", m.Path, m.Backend, err)
		}

		transport, err := newTransport(m.ProxyClientCert, m.ProxyClientKey, m.BackendServerCA)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: %w", m.Path, err)
		}

		var handler http.Handler
		if m.Path == "/clusters/" {
			clusterProxy := newShardReverseProxy()
			clusterProxy.Transport = transport
			handler = shardHandler(index, clusterProxy)
		} else {
			// TODO: handle virtual workspace apiservers per shard
			proxy := httputil.NewSingleHostReverseProxy(u)
			proxy.Transport = transport
			handler = proxy
		}

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

		handler = WithProxyAuthHeaders(handler, userHeader, groupHeader, extraHeaderPrefix)

		logger.V(2).WithValues("path", m.Path).Info("adding handler")
		if m.Path == "/" {
			handlers.defaultHandler = handler
		} else {

			handlers.mapping = append(handlers.mapping, httpHandlerMapping{
				weight:  len(m.Path),
				path:    m.Path,
				handler: handler,
			})
		}
	}

	handlers.mapping = sortMappings(handlers.mapping)

	return &handlers, nil
}

func sortMappings(mappings []httpHandlerMapping) []httpHandlerMapping {
	// sort mappings by weight
	// higher weight means that the mapping is more specific and should be matched first
	// Example: /clusters/cluster1/ will be matched before /clusters/
	for i := range mappings {
		for j := range mappings {
			if mappings[i].weight > mappings[j].weight {
				mappings[i], mappings[j] = mappings[j], mappings[i]
			}
		}
	}
	return mappings
}
