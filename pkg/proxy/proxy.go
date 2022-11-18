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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"k8s.io/apimachinery/pkg/util/runtime"
	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

func newTransport(clientCert, clientKeyFile, caFile string) (*http.Transport, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file %q: %w", caFile, err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	cert, err := tls.LoadX509KeyPair(clientCert, clientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate %q or key %q: %w", clientCert, clientKeyFile, err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}

	return transport, nil
}

// WithProxyAuthHeaders does client cert termination by extracting the user and groups and
// passing them through access headers to the shard.
func WithProxyAuthHeaders(delegate http.Handler, userHeader, groupHeader string, extraHeaderPrefix string) http.HandlerFunc {
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

func newShardReverseProxy() *httputil.ReverseProxy {
	director := func(req *http.Request) {
		shardURL := ShardURLFrom(req.Context())
		if shardURL == nil {
			// should not happen if wiring is correct
			runtime.HandleError(fmt.Errorf("no shard URL found in request context"))
			req.URL.Scheme = "https"
			req.URL.Host = "notfound"
			return
		}

		canonicalPath := tenancy.CanonicalPathFrom(req.Context())

		req.Header.Set(tenancy.XKcpCanonicalPathHeader, canonicalPath.String())
		req.URL.Scheme = shardURL.Scheme
		req.URL.Host = shardURL.Host
	}
	return &httputil.ReverseProxy{Director: director}
}

type shardKey int

const shardContextKey shardKey = iota

func WithShardURL(parent context.Context, shardURL *url.URL) context.Context {
	return context.WithValue(parent, shardContextKey, shardURL)
}

func ShardURLFrom(ctx context.Context) *url.URL {
	shardURL, ok := ctx.Value(shardContextKey).(*url.URL)
	if !ok {
		return nil
	}
	return shardURL
}
