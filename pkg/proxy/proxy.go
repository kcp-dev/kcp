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
	"net/http/httputil"
	"net/url"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
)

// KCPProxy wraps the httputil.ReverseProxy and captures the backend name.
type KCPProxy struct {
	proxy   *httputil.ReverseProxy
	backend string
}

// NewReverseProxy returns a new reverse proxy where backend is the backend URL to
// connect to, clientCert is the proxy's client cert to use to connect to it,
// clientKeyFile is the proxy's client private key file, and caFile is the CA
// the proxy uses to verify the backend server's cert.
func NewReverseProxy(backend, clientCert, clientKeyFile, caFile string) (*KCPProxy, error) {
	target, err := url.Parse(backend)
	if err != nil {
		return nil, err
	}

	caCert, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	cert, err := tls.LoadX509KeyPair(clientCert, clientKeyFile)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport

	return &KCPProxy{proxy: proxy, backend: backend}, nil
}

// ProxyHandler extracts the CN as a user name and Organizations as groups from
// the client cert and adds them as HTTP headers to backend request.
func ProxyHandler(p *KCPProxy, UserHeader, GroupHeader string) func(wr http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, ok := request.UserFrom(r.Context()); ok {
			appendClientCertAuthHeaders(r.Header, u, UserHeader, GroupHeader)
		}
		if klog.V(6).Enabled() {
			klog.Infof("%s %s (%s -> %s) ", r.Method, r.RequestURI, r.RemoteAddr, p.backend)
		}
		p.proxy.ServeHTTP(w, r)
	}
}

func appendClientCertAuthHeaders(header http.Header, user userinfo.Info, UserHeader, GroupHeader string) {
	header.Set(UserHeader, user.GetName())

	for _, group := range user.GetGroups() {
		header.Add(GroupHeader, group)
	}
}
