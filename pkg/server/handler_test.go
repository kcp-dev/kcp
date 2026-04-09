/*
Copyright 2026 The kcp Authors.

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

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	clientgotransport "k8s.io/client-go/transport"
	"k8s.io/klog/v2"
)

func TestWithVirtualWorkspacesProxyUsesRequestScopedImpersonationTransport(t *testing.T) {
	t.Parallel()

	targetURL, err := url.Parse("https://virtual-workspaces.example")
	require.NoError(t, err)

	baseTransport := &recordingRoundTripper{}
	proxy := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = targetURL.Scheme
			r.URL.Host = targetURL.Host
			delete(r.Header, "X-Forwarded-For")
		},
	}

	handler := WithVirtualWorkspacesProxy(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			t.Fatalf("unexpected fallback to API handler for %s", req.URL.Path)
		}),
		targetURL,
		baseTransport,
		proxy,
	)

	aliceEntered := make(chan struct{})
	aliceRelease := make(chan struct{})

	aliceReq := proxiedRequest(
		t,
		"/services/alice",
		"alice",
		logr.New(&blockingLogSink{
			entered: aliceEntered,
			release: aliceRelease,
		}),
	)
	bobReq := proxiedRequest(
		t,
		"/services/bob",
		"bob",
		logr.New(&blockingLogSink{}),
	)

	aliceRecorder := httptest.NewRecorder()
	bobRecorder := httptest.NewRecorder()

	aliceDone := make(chan struct{})
	go func() {
		defer close(aliceDone)
		handler.ServeHTTP(aliceRecorder, aliceReq)
	}()

	select {
	case <-aliceEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first proxied request to reach the logging barrier")
	}

	bobDone := make(chan struct{})
	go func() {
		defer close(bobDone)
		handler.ServeHTTP(bobRecorder, bobReq)
	}()

	select {
	case <-bobDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the second proxied request to complete")
	}

	close(aliceRelease)

	select {
	case <-aliceDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first proxied request to complete")
	}

	require.Equal(t, http.StatusOK, aliceRecorder.Code)
	require.Equal(t, http.StatusOK, bobRecorder.Code)
	require.Equal(t, map[string]string{
		"/services/alice": "alice",
		"/services/bob":   "bob",
	}, baseTransport.UsersByPath())
}

func proxiedRequest(t *testing.T, path, userName string, logger logr.Logger) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "https://kcp.example"+path, nil)
	ctx := request.WithUser(req.Context(), &userinfo.DefaultInfo{Name: userName})
	ctx = klog.NewContext(ctx, logger)
	return req.WithContext(ctx)
}

type recordedRequest struct {
	path string
	user string
}

type recordingRoundTripper struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.requests = append(r.requests, recordedRequest{
		path: req.URL.Path,
		user: req.Header.Get(clientgotransport.ImpersonateUserHeader),
	})
	r.mu.Unlock()

	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader("ok")),
		ContentLength: 2,
		Request:       req,
	}, nil
}

func (r *recordingRoundTripper) UsersByPath() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()

	usersByPath := make(map[string]string, len(r.requests))
	for _, req := range r.requests {
		usersByPath[req.path] = req.user
	}

	return usersByPath
}

type blockingLogSink struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *blockingLogSink) Init(logr.RuntimeInfo) {}

func (s *blockingLogSink) Enabled(level int) bool {
	return true
}

func (s *blockingLogSink) Info(level int, msg string, keysAndValues ...any) {
	if level != 4 || s.release == nil {
		return
	}

	s.once.Do(func() {
		close(s.entered)
	})

	<-s.release
}

func (s *blockingLogSink) Error(err error, msg string, keysAndValues ...any) {}

func (s *blockingLogSink) WithValues(keysAndValues ...any) logr.LogSink {
	return s
}

func (s *blockingLogSink) WithName(name string) logr.LogSink {
	return s
}
