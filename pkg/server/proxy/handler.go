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

package proxy

import (
	"net/http"
	"strings"

	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

type HttpHandler struct {
	Mappings       types.HttpHandlerMappings
	DefaultHandler http.Handler
}

func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// we already parsed and looked up the cluster in the clusterresolver middleware,
	// and potentially have stored the shard URL in the context already
	shardURL := lookup.ShardURLFrom(r.Context())
	if shardURL != nil {
		r.URL = shardURL
	}

	mux := http.NewServeMux()

	// fallback for all unrecognized URLs
	if h.DefaultHandler != nil {
		mux.Handle("/", h.DefaultHandler)
	}

	for _, mapping := range h.Mappings {
		p := strings.TrimRight(mapping.Path, "/")

		if p == "/clusters" {
			mux.Handle("/clusters/{cluster}", mapping.Handler)
			mux.Handle("/clusters/{cluster}/{trail...}", mapping.Handler)
		} else {
			mux.Handle(p, mapping.Handler)
			mux.Handle(p+"/{trail...}", mapping.Handler)
		}
	}

	mux.ServeHTTP(w, r)
}
