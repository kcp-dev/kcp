/*
Copyright 2024 The KCP Authors.

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
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
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
	Index          index.Index
	Mappings       HttpHandlerMappings
	DefaultHandler http.Handler
}

// httpHandlerMapping is used to route traffic to the correct backend server.
// Higher weight means that the mapping is more specific and should be matched first.
type HttpHandlerMapping struct {
	Weight  int
	Path    string
	Handler http.Handler
}

type HttpHandlerMappings []HttpHandlerMapping

// Sort mappings by weight
// higher weight means that the mapping is more specific and should be matched first
// Example: /clusters/cluster1/ will be matched before /clusters/ .
func (h HttpHandlerMappings) Sort() {
	for i := range h {
		for j := range h {
			if h[i].Weight > h[j].Weight {
				h[i], h[j] = h[j], h[i]
			}
		}
	}
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
	// Example: /clusters/cluster1/ will be matched before /clusters/ .
	for _, m := range h.Mappings {
		url, errorCode := h.resolveURL(r)
		if errorCode != 0 {
			http.Error(w, http.StatusText(errorCode), errorCode)
			return
		}
		if strings.HasPrefix(url, m.Path) {
			m.Handler.ServeHTTP(w, r)
			return
		}
	}

	h.DefaultHandler.ServeHTTP(w, r)
}

func (h *HttpHandler) resolveURL(r *http.Request) (string, int) {
	// if we don't match any of the paths, use the default behavior - request
	var cs = strings.SplitN(strings.TrimLeft(r.URL.Path, "/"), "/", 3)
	if len(cs) < 2 || cs[0] != "clusters" {
		return r.URL.Path, 0
	}

	clusterPath := logicalcluster.NewPath(cs[1])
	if !clusterPath.IsValid() {
		return r.URL.Path, 0
	}

	result, found := h.Index.LookupURL(clusterPath)
	if result.ErrorCode != 0 {
		return "", result.ErrorCode
	}
	if found {
		u, err := url.Parse(result.URL)
		if err == nil && u != nil {
			u.Path = strings.TrimSuffix(u.Path, "/")
			r.URL.Path = path.Join(u.Path, strings.Join(cs[2:], "/")) // override request prefix and keep kube api contextual suffix
			return u.Path, 0
		}
	}

	return r.URL.Path, 0
}
