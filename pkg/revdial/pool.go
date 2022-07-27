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
	mu   sync.Mutex
	pool map[string]*Dialer
}

// NewReversePool returns a ReversePool
func NewReversePool() *ReversePool {
	return &ReversePool{
		pool: map[string]*Dialer{},
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

// HTTP Handler that handles reverse connections and reverse proxy requests using 2 different paths:
// path base/revdial?key=id establish reverse connections and queue them so it can be consumed by the dialer
// path base/proxy/id/(path) proxies the (path) through the reverse connection identified by id
func (rp *ReversePool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// process path
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(path) == 0 {
		http.Error(w, "", http.StatusNotFound)
		return
	}
	// route the request
	pos := -1
	for i := len(path) - 1; i >= 0; i-- {
		p := path[i]
		// pathRevDial comes with a param
		if p == pathRevDial {
			if i != len(path)-1 {
				http.Error(w, "revdial: only last element on path allowed", http.StatusInternalServerError)
				return
			}
			pos = i
			break
		}
		// pathRevProxy requires at least the id subpath
		if p == pathRevProxy {
			if i == len(path)-1 {
				http.Error(w, "proxy: reverse path id required", http.StatusInternalServerError)
				return
			}
			pos = i
			break
		}
	}
	if pos < 0 {
		http.Error(w, "revdial: not handler ", http.StatusNotFound)
		return
	}
	// Forward proxy /base/proxy/id/..proxied path...
	if path[pos] == pathRevProxy {
		id := path[pos+1]
		target, err := url.Parse("http://" + id)
		if err != nil {
			http.Error(w, "wrong url", http.StatusInternalServerError)
			return
		}
		d := rp.GetDialer(id)
		if d == nil {
			http.Error(w, "not reverse connections for this id available", http.StatusInternalServerError)
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
			// strip the non-proxied path
			proxypath := "/"
			if len(path) > pos+1 {
				proxypath += strings.Join(path[pos+2:], "/")
			}
			req.URL.Path = proxypath
			// strip authorization header
			req.Header.Del("Authorization")
			director(req)
		}
		proxy.ServeHTTP(w, r)
		klog.V(5).Infof("proxy server closed %v ", err)
	} else {
		// The caller identify itself by the value of the keu
		// https://server/revdial?id=dialerUniq
		dialerUniq := r.URL.Query().Get(urlParamKey)
		if len(dialerUniq) == 0 {
			http.Error(w, "only reverse connections with id supported", http.StatusInternalServerError)
			return
		}

		d := rp.GetDialer(dialerUniq)
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
			rp.DeleteDialer(dialerUniq)
			rp.CreateDialer(dialerUniq, conn)
			// start control loop
			select {
			case <-r.Context().Done():
				conn.Close()
			case <-doneCh:
			}
			klog.V(5).Infof("stopped dialer %s control connection ", dialerUniq)
			return

		}
		// create a reverse connection
		klog.V(5).Infof("created reverse connection to %s %s id %s", r.RequestURI, r.RemoteAddr, dialerUniq)
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
