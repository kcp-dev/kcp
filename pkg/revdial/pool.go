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

package revdial

import (
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"k8s.io/klog/v2"
)

type controlMsg struct {
	Command  string `json:"command,omitempty"`  // "keep-alive", "conn-ready", "pickup-failed"
	ConnPath string `json:"connPath,omitempty"` // conn pick-up URL path for "conn-url", "pickup-failed"
	Err      string `json:"err,omitempty"`
}

// ReversePool contains a pool of Dialers to create reverse connections
// It exposes an http.Handler to handle the clients.
// 	pool := NewReversePool()
// 	mux := http.NewServeMux()
//	mux.Handle("", pool)
type ReversePool struct {
	mu         sync.Mutex
	pool       map[string]*Dialer
	apiHandler http.Handler
}

// NewReversePool returns a ReversePool
func NewReversePool(apiHandler http.Handler) *ReversePool {
	return &ReversePool{
		pool:       map[string]*Dialer{},
		apiHandler: apiHandler,
	}
}

// Close the Reverse pool and all its dialers
func (rp *ReversePool) Close() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	for _, v := range rp.pool {
		v.Close()
	}
}

// GetDialer returns a reverse dialer for the id
func (rp *ReversePool) GetDialer(id string) *Dialer {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.pool[id]
}

// CreateDialer creates a reverse dialer with id
// it's a noop if a dialer already exists
func (rp *ReversePool) CreateDialer(id string, conn net.Conn) *Dialer {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if d, ok := rp.pool[id]; ok {
		return d
	}
	d := NewDialer(id, conn)
	rp.pool[id] = d
	return d

}

// DeleteDialer delete the reverse dialer for the id
func (rp *ReversePool) DeleteDialer(id string) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	delete(rp.pool, id)
}

func TunnelParametersHandler(apiHandler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// process path
		path := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(path) == 0 {
			apiHandler.ServeHTTP(w, req)
			return
		}

		// route the request
		pos := -1
		for i := len(path) - 1; i >= 0; i-- {
			p := path[i]
			// pathRevProxy requires at least the id subpath
			if p == pathRevProxy {
				if i == len(path)-1 {
					http.Error(w, "proxy: path is missing", http.StatusInternalServerError)
					return
				}
				pos = i
				break
			}
			// pathRevDial comes with a param
			if p == pathRevDial {
				if i != len(path)-1 {
					http.Error(w, "revdial: only last element on path allowed", http.StatusInternalServerError)
					return
				}
				pos = i
				break
			}
		}
		// fall through
		if pos < 0 {
			apiHandler.ServeHTTP(w, req)
			return
		}
		ctx := req.Context()
		// Forward proxy /base/proxy/id/..proxied path...
		if path[pos] == pathRevProxy {
			// strip the non-proxied path
			proxypath := "/"
			if len(path) > pos+1 {
				proxypath += strings.Join(path[pos+2:], "/")
			}
			tunnelID := path[pos+1]
			ctx = WithTunnelID(ctx, tunnelID)
			ctx = WithTunnelPath(ctx, proxypath)
		} else {
			// The caller identify itself by the value of the keu
			// https://server/revdial?id=tunnelID
			tunnelID := req.URL.Query().Get(urlParamKey)
			if len(tunnelID) == 0 {
				http.Error(w, "only reverse connections with id supported", http.StatusInternalServerError)
				return
			}
			ctx = WithTunnelID(ctx, tunnelID)
		}

		req = req.WithContext(ctx)
		apiHandler.ServeHTTP(w, req)
	}
}

// HTTP Handler that handles reverse connections and reverse proxy requests using 2 different paths:
// path base/revdial?key=id establish reverse connections and queue them so it can be consumed by the dialer
// path base/proxy/id/(path) proxies the (path) through the reverse connection identified by id
func (rp *ReversePool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tunnelID := TunnelIDFrom(ctx)
	tunnelPath := TunnelPathFrom(ctx)

	switch {
	// this is a request that we should reverse proxy
	case tunnelID != "" && tunnelPath != "":
		d := rp.GetDialer(tunnelID)
		if d == nil {
			http.Error(w, "not reverse connections for this id available", http.StatusInternalServerError)
			return
		}
		target, err := url.Parse("http://" + tunnelID)
		if err != nil {
			http.Error(w, "wrong url", http.StatusInternalServerError)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		director := proxy.Director
		proxy.Transport = &http.Transport{
			Proxy:               nil,    // no proxies
			DialContext:         d.Dial, // use a reverse connection
			ForceAttemptHTTP2:   false,  // this is a tunneled connection
			DisableKeepAlives:   true,   // one connection per reverse connection
			MaxIdleConnsPerHost: -1,
		}
		// only proxy the proxied path and don't forward the authentication header
		proxy.Director = func(req *http.Request) {
			req.URL.Path = tunnelPath
			// strip authorization header
			req.Header.Del("Authorization")
			director(req)
		}
		proxy.ServeHTTP(w, r)
		klog.V(5).Infof("proxy server closed %v ", err)
	// this is a tunnel establishment request
	case tunnelID != "" && tunnelPath == "":
		d := rp.GetDialer(tunnelID)
		// First flush response headers
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher not implemented", http.StatusInternalServerError)
			return
		}

		// first connection to register the dialer and start the control loop
		fw := &flushWriter{w: w, f: flusher}
		doneCh := make(chan struct{})
		conn := rwconn.NewConn(r.Body, fw, rwconn.SetWriteDelay(500*time.Millisecond), rwconn.SetCloseHook(func() {
			// exit the handler
			close(doneCh)
		}))
		if d == nil || isClosedChan(d.Done()) {
			// start clean
			rp.DeleteDialer(tunnelID)
			rp.CreateDialer(tunnelID, conn)
			// start control loop
			select {
			case <-r.Context().Done():
				conn.Close()
			case <-doneCh:
			}
			klog.V(5).Infof("stopped dialer %s control connection ", tunnelID)
			return
		}
		// create a reverse connection
		klog.V(5).Infof("created reverse connection to %s %s id %s", r.RequestURI, r.RemoteAddr, tunnelID)
		select {
		case d.incomingConn <- conn:
		case <-d.Done():
			http.Error(w, "Reverse dialer closed", http.StatusInternalServerError)
			return
		}
		// keep the handler alive until the connection is closed
		select {
		case <-r.Context().Done():
			conn.Close()
		case <-doneCh:
		}
		klog.V(5).Infof("Connection from %s done", r.RemoteAddr)
	// fall through
	case tunnelID == "" && tunnelPath == "":
		rp.apiHandler.ServeHTTP(w, r)
	default:
		http.Error(w, "tunnel ID and tunnel Path parameters are incorrect", http.StatusInternalServerError)
	}
}

// flushWriter
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (w *flushWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	w.f.Flush()
	return n, err
}

func (w *flushWriter) Close() error {
	return nil
}

func isClosedChan(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
