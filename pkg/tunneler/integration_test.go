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
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T) (*http.Client, string, func()) {
	t.Helper()
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello world")
	}))
	backend.EnableHTTP2 = true
	backend.StartTLS()

	// public server
	mux := http.NewServeMux()
	apiHandler := WithSyncerTunnel(mux)
	publicServer := httptest.NewUnstartedServer(apiHandler)
	publicServer.EnableHTTP2 = true
	publicServer.StartTLS()

	// private server
	dstUrl, err := SyncerTunnelURL(publicServer.URL, "ws", "d001")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewListener(publicServer.Client(), dstUrl)
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
	return publicServer.Client(), dstUrl + "/" + cmdTunnelProxy + "/", stop

}

func Test_integration(t *testing.T) {
	client, uri, stop := setup(t)
	resp, err := client.Get(uri)
	if err != nil {
		t.Fatalf("Request Failed: %s", err)
	}
	defer resp.Body.Close()
	defer stop()

	body, err := ioutil.ReadAll(resp.Body)
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
	client, uri, stop := setup(t)
	defer stop()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(uri)
			if err != nil {
				t.Errorf("Request Failed: %s", err)
			}
			defer resp.Body.Close()

			body, err := ioutil.ReadAll(resp.Body)
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
	apiHandler := WithSyncerTunnel(mux)
	publicServer := httptest.NewUnstartedServer(apiHandler)
	publicServer.EnableHTTP2 = true
	publicServer.StartTLS()
	defer publicServer.Close()

	// private server
	dstUrl, err := SyncerTunnelURL(publicServer.URL, "ws", "d001")
	if err != nil {
		t.Fatal(err)
	}
	l, err := NewListener(publicServer.Client(), dstUrl)
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

	client := publicServer.Client()
	uri := dstUrl + "/" + cmdTunnelProxy + "/"

	resp, err := client.Get(uri)
	if err != nil {
		t.Fatalf("Request Failed: %s", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
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

	l2, err := NewListener(publicServer.Client(), dstUrl)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	server2 := &http.Server{Handler: proxy}
	//nolint:errcheck
	go server2.Serve(l2)
	defer server2.Close()

	resp, err = client.Get(uri)
	if err != nil {
		t.Fatalf("Request Failed: %s", err)
	}
	defer resp.Body.Close()

	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Reading body failed: %s", err)
	}
	// Log the request body
	bodyString = string(body)
	if bodyString != "Hello world" {
		t.Errorf("Expected %s received %s", "Hello world", bodyString)
	}
}
