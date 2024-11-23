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

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
)

func NewHandler(ctx context.Context, o *proxyoptions.Options, index index.Index) (http.Handler, error) {
	mappingData, err := os.ReadFile(o.MappingFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read mapping file %q: %w", o.MappingFile, err)
	}

	var mapping []proxy.PathMapping
	if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mapping file %q: %w", o.MappingFile, err)
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
