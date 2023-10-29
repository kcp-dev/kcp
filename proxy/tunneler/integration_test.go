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

package tunneler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
)

// requestInfoHandler is a helping function to populate the requestInfo of a request as expected
// by the WithSyncerTunnelHandler.
func requestInfoHandler(handler http.Handler) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := genericapirequest.RequestInfoFrom(ctx); ok {
			handler.ServeHTTP(w, r)
			return
		}
		r = r.WithContext(genericapirequest.WithRequestInfo(ctx,
			&genericapirequest.RequestInfo{
				IsResourceRequest: true,
				APIGroup:          "workload.kcp.io",
				APIVersion:        "v1alpha1",
				Resource:          "synctargets",
				Subresource:       "tunnel",
				Name:              "d001",
			},
		))
		r = r.WithContext(genericapirequest.WithCluster(r.Context(), genericapirequest.Cluster{Name: "ws"}))
		handler.ServeHTTP(w, r)
	})
}

func setup(t *testing.T) (string, *tunneler, func()) {
	t.Helper()
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello world")
	}))
	backend.EnableHTTP2 = true
	backend.StartTLS()

	// public server
	mux := http.NewServeMux()
	tunneler := NewTunneler()
	apiHandler := tunneler.WithProxyTunnelHandler(mux)
	apiHandler = requestInfoHandler(apiHandler)
	publicServer := httptest.NewUnstartedServer(apiHandler)
	publicServer.EnableHTTP2 = true
	publicServer.StartTLS()

	// private server
	dstURL, err := ProxyTunnelURL(publicServer.URL, "ws", "d001")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewListener(publicServer.Client(), dstURL)
	if err != nil {
		t.Fatal(err)
	}

	// reverse proxy queries to an internal host
	url, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.Transport = backend.Client().Transport
	server := &http.Server{Handler: proxy}
	//nolint:errcheck
	go server.Serve(l)

	// client
	// wait for the reverse connection to be established
	time.Sleep(1 * time.Second)
	stop := func() {
		l.Close()
		server.Close()
		publicServer.Close()
		backend.Close()
	}
	return dstURL, tunneler, stop
}

func Test_integration(t *testing.T) {
	uri, tunneler, stop := setup(t)
	rw := httptest.NewRecorder()
	b := &bytes.Buffer{}
	req, err := http.NewRequest(http.MethodGet, uri, b) //nolint:noctx
	require.NoError(t, err)
	tunneler.Proxy("ws", "d001", rw, req)
	defer stop()

	response := rw.Result()
	body, err := io.ReadAll(response.Body)
	defer response.Body.Close()
	if err != nil {
		t.Fatalf("Reading body failed: %s", err)
	}

	// Log the request body
	bodyString := string(body)
	if bodyString != "Hello world" {
		t.Errorf("Expected %s received %s", "Hello world", bodyString)
	}
}

func Test_integration_multiple_connections(t *testing.T) {
	uri, tunneler, stop := setup(t)
	defer stop()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rw := httptest.NewRecorder()
			b := &bytes.Buffer{}
			req, err := http.NewRequest(http.MethodGet, uri, b) //nolint:noctx
			require.NoError(t, err)
			tunneler.Proxy("ws", "d001", rw, req)

			response := rw.Result()
			body, err := io.ReadAll(response.Body)
			defer response.Body.Close()
			if err != nil {
				t.Errorf("Reading body failed: %s", err)
			}

			// Log the request body
			bodyString := string(body)
			if bodyString != "Hello world" {
				t.Errorf("Expected %s received %s", "Hello world", bodyString)
			}
		}()
	}
	wg.Wait()
}

func Test_integration_listener_reconnect(t *testing.T) {
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello world")
	}))
	backend.EnableHTTP2 = true
	backend.StartTLS()
	defer backend.Close()

	// public server
	mux := http.NewServeMux()
	tunneler := NewTunneler()
	apiHandler := tunneler.WithProxyTunnelHandler(mux)
	apiHandler = requestInfoHandler(apiHandler)
	publicServer := httptest.NewUnstartedServer(apiHandler)
	publicServer.EnableHTTP2 = true
	publicServer.StartTLS()
	defer publicServer.Close()

	// private server
	dstURL, err := ProxyTunnelURL(publicServer.URL, "ws", "d001")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewListener(publicServer.Client(), dstURL)
	if err != nil {
		t.Fatal(err)
	}

	// reverse proxy queries to an internal host
	url, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.Transport = backend.Client().Transport
	server := &http.Server{Handler: proxy}
	//nolint:errcheck
	go server.Serve(l)
	defer server.Close()

	// client
	// wait for the reverse connection to be established
	time.Sleep(1 * time.Second)

	rw := httptest.NewRecorder()
	b := &bytes.Buffer{}
	req, err := http.NewRequest(http.MethodGet, dstURL, b) //nolint:noctx
	require.NoError(t, err)
	tunneler.Proxy("ws", "d001", rw, req)

	response := rw.Result()
	body, err := io.ReadAll(response.Body)
	defer response.Body.Close()
	if err != nil {
		t.Fatalf("Reading body failed: %s", err)
	}
	// Log the request body
	bodyString := string(body)
	if bodyString != "Hello world" {
		t.Errorf("Expected %s received %s", "Hello world", bodyString)
	}

	// reconnect
	server.Close()
	l.Close()
	<-l.donec
	l = nil

	l2, err := NewListener(publicServer.Client(), dstURL)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	server2 := &http.Server{Handler: proxy}
	//nolint:errcheck
	go server2.Serve(l2)
	defer server2.Close()

	rw2 := httptest.NewRecorder()
	b2 := &bytes.Buffer{}
	req2, err := http.NewRequest(http.MethodGet, dstURL, b2) //nolint:noctx
	require.NoError(t, err)
	tunneler.Proxy("ws", "d001", rw2, req2)

	response = rw2.Result()
	body, err = io.ReadAll(response.Body)
	defer response.Body.Close()
	if err != nil {
		t.Fatalf("Reading body failed: %s", err)
	}

	// Log the request body
	bodyString = string(body)
	if bodyString != "Hello world" {
		t.Errorf("Expected %s received %s", "Hello world", bodyString)
	}
}
