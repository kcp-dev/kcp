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

	"sigs.k8s.io/yaml"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
)

func loadMappings(filename string) ([]proxy.PathMapping, error) {
	mappingData, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var mapping []proxy.PathMapping
	if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
		return nil, err
	}

	return mapping, nil
}

func isShardMapping(m proxy.PathMapping) bool {
	return m.Path == "/clusters/"
}

func NewHandler(ctx context.Context, mappings []proxy.PathMapping, index index.Index) (http.Handler, error) {
	handlers := proxy.HttpHandler{
		Index: index,
		Mappings: proxy.HttpHandlerMappings{
			{
				Weight:  0,
				Path:    "/metrics",
				Handler: legacyregistry.Handler(),
			},
		},
	}

	logger := klog.FromContext(ctx)
	for _, m := range mappings {
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
		if isShardMapping(m) {
			clusterProxy := newShardReverseProxy()
			clusterProxy.Transport = transport
			handler = clusterProxy
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
			handlers.DefaultHandler = handler
		} else {
			handlers.Mappings = append(handlers.Mappings, proxy.HttpHandlerMapping{
				Weight:  len(m.Path),
				Path:    m.Path,
				Handler: handler,
			})
		}
	}

	handlers.Mappings.Sort()

	return &handlers, nil
}
