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
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"
)

type controlMsg struct {
	Command  string `json:"command,omitempty"`  // "keep-alive", "conn-ready", "pickup-failed"
	ConnPath string `json:"connPath,omitempty"` // conn pick-up URL path for "conn-url", "pickup-failed"
	Err      string `json:"err,omitempty"`
}

type key struct {
	clusterName    logicalcluster.Name
	syncTargetName string
}

// tunneler contains a pool of Dialers to create reverse connections
// based on the cluster and syncer name.
type tunneler struct {
	mu   sync.Mutex
	pool map[key]*Dialer
}

func NewTunneler() *tunneler {
	return &tunneler{
		pool: make(map[key]*Dialer),
		mu:   sync.Mutex{},
	}
}

// getDialer returns a reverse dialer for the id.
func (tn *tunneler) getDialer(clusterName logicalcluster.Name, syncTargetName string) *Dialer {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	id := key{clusterName, syncTargetName}
	return tn.pool[id]
}

// createDialer creates a reverse dialer with id
// it's a noop if a dialer already exists.
func (tn *tunneler) createDialer(clusterName logicalcluster.Name, syncTargetName string, conn net.Conn) *Dialer {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	id := key{clusterName, syncTargetName}
	if d, ok := tn.pool[id]; ok {
		return d
	}
	d := NewDialer(conn)
	tn.pool[id] = d
	return d
}

// deleteDialer deletes the reverse dialer for a given id.
func (tn *tunneler) deleteDialer(clusterName logicalcluster.Name, syncTargetName string) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	id := key{clusterName, syncTargetName}
	delete(tn.pool, id)
}

// flushWriter.
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

// Proxy proxies the request to the syncer identified by the cluster and syncername.
func (tn *tunneler) Proxy(clusterName logicalcluster.Name, syncerName string, rw http.ResponseWriter, req *http.Request) {
	d := tn.getDialer(clusterName, syncerName)
	if d == nil || isClosedChan(d.Done()) {
		rw.Header().Set("Retry-After", "1")
		http.Error(rw, "proxy tunnels: tunnel closed", http.StatusServiceUnavailable)
		return
	}

	target, err := url.Parse("http://" + syncerName)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		Proxy:               nil,    // no proxies
		DialContext:         d.Dial, // use a reverse connection
		ForceAttemptHTTP2:   false,  // this is a tunneled connection
		DisableKeepAlives:   true,   // one connection per reverse connection
		MaxIdleConnsPerHost: -1,
	}

	proxy.ServeHTTP(rw, req)
}
