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
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"

	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// Server holds the configuration for the proxy server
type Server struct {
	ListenAddress  string // The hostname:port for the proxy to listen on
	ClientCACert   string // CA used to validate client certs connecting to the proxy
	ServerCertFile string // The proxy's server cert
	ServerKeyFile  string // The proxy's private key file
	MappingFile    string // A yaml file containing a list of PathMappings
}

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

// Serve sets up the proxy and starts serving
func (s *Server) Serve() error {
	mappingData, err := ioutil.ReadFile(s.MappingFile)
	if err != nil {
		klog.Errorf("Couldn't read mapping file: %s", s.MappingFile)
		return err
	}

	var mapping []PathMapping
	if err = yaml.Unmarshal(mappingData, &mapping); err != nil {
		return err
	}

	for _, pathCfg := range mapping {
		klog.V(2).Infof("Adding %v", pathCfg)
		proxy, err := NewReverseProxy(pathCfg.Backend, pathCfg.ProxyClientCert, pathCfg.ProxyClientKey, pathCfg.BackendServerCA)
		if err != nil {
			return err
		}
		userHeader := "X-Remote-User"
		groupHeader := "X-Remote-Group"
		if pathCfg.UserHeader != "" {
			userHeader = pathCfg.UserHeader
		}
		if pathCfg.GroupHeader != "" {
			groupHeader = pathCfg.GroupHeader
		}
		http.Handle(pathCfg.Path, http.HandlerFunc(ProxyHandler(proxy, userHeader, groupHeader)))
	}

	clientCACert, err := ioutil.ReadFile(s.ClientCACert)
	if err != nil {
		klog.Errorf("Couldn't read client CA: %s", s.ClientCACert)
		return err
	}

	clientCACertPool := x509.NewCertPool()
	clientCACertPool.AppendCertsFromPEM(clientCACert)

	server := &http.Server{
		Addr:    s.ListenAddress,
		Handler: http.DefaultServeMux,
		TLSConfig: &tls.Config{
			ClientAuth: tls.VerifyClientCertIfGiven,
			ClientCAs:  clientCACertPool,
		},
	}

	klog.V(1).Infof("Listening on %s", server.Addr)
	return server.ListenAndServeTLS(s.ServerCertFile, s.ServerKeyFile)
}
