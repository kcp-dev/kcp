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
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"

	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
)

// PathMapping describes how to route traffic from a path to a backend server.
// Each Path is registered with the DefaultServeMux with a handler that
// delegates to the specified backend.
type PathMapping struct {
	Path            string `json:"path"`
	Backend         string `json:"backend"`
	BackendServerCA string `json:"backend_server_ca"`
	ProxyClientCert string `json:"proxy_client_cert"`
	ProxyClientKey  string `json:"proxy_client_key"`
	UserHeader      string `json:"user_header,omitempty"`
	GroupHeader     string `json:"group_header,omitempty"`
}

func NewHandler(o *proxyoptions.Options, index index.Index) (http.Handler, error) {
	mappingData, err := ioutil.ReadFile(o.MappingFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read mapping file %q: %w", o.MappingFile, err)
	}

	var mapping []PathMapping
	if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mapping file %q: %w", o.MappingFile, err)
	}

	mux := http.NewServeMux()

	// TODO: implement proper readyz handler
	mux.Handle("/readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))

	// TODO: implement proper livez handler
	mux.Handle("/livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))

	for _, m := range mapping {
		klog.V(2).Infof("Adding mapping %v", m)

		u, err := url.Parse(m.Backend)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: failed to parse URL %q: %w", m.Path, m.Backend, err)
		}

		transport, err := newTransport(m.ProxyClientCert, m.ProxyClientKey, m.BackendServerCA)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: %w", m.Path, err)
		}

		var handler http.HandlerFunc
		if m.Path == "/clusters/" {
			clusterProxy := newShardReverseProxy()
			clusterProxy.Transport = transport
			handler = shardHandler(index, clusterProxy)
		} else {
			// TODO: handle virtual workspace apiservers per shard
			proxy := httputil.NewSingleHostReverseProxy(u)
			proxy.Transport = transport
			handler = proxy.ServeHTTP
		}

		userHeader := "X-Remote-User"
		groupHeader := "X-Remote-Group"
		if m.UserHeader != "" {
			userHeader = m.UserHeader
		}
		if m.GroupHeader != "" {
			groupHeader = m.GroupHeader
		}

		handler = WithProxyAuthHeaders(handler, userHeader, groupHeader)

		mux.Handle(m.Path, handler)
	}

	return mux, nil
}
