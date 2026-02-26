/*
Copyright 2024 The kcp Authors.

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

package types

import "net/http"

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
