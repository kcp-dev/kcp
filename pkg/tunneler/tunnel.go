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
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	defaultTunnelPathPrefix = "/services/syncer-tunnels/clusters"
	cmdTunnelConnect        = "connect"
	cmdTunnelProxy          = "proxy"
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

// tunnelPool contains a pool of Dialers to create reverse connections
// based on the workspace and syncer name.
type tunnelPool struct {
	mu   sync.Mutex
	pool map[key]*Dialer
}

// NewtunnelPool returns a tunnelPool.
func newTunnelPool() *tunnelPool {
	return &tunnelPool{
		pool: map[key]*Dialer{},
	}
}

// getDialer returns a reverse dialer for the id.
func (rp *tunnelPool) getDialer(clusterName logicalcluster.Name, syncTargetName string) *Dialer {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	id := key{clusterName, syncTargetName}
	return rp.pool[id]
}

// createDialer creates a reverse dialer with id
// it's a noop if a dialer already exists.
func (rp *tunnelPool) createDialer(clusterName logicalcluster.Name, syncTargetName string, conn net.Conn) *Dialer {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	id := key{clusterName, syncTargetName}
	if d, ok := rp.pool[id]; ok {
		return d
	}
	d := NewDialer(conn)
	rp.pool[id] = d
	return d
}

// deleteDialer delete the reverse dialer for the id.
func (rp *tunnelPool) deleteDialer(clusterName logicalcluster.Name, syncTargetName string) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	id := key{clusterName, syncTargetName}
	delete(rp.pool, id)
}

// SyncerTunnelProxyPath builds the path for the proxy handler as expected by the Dialer
func SyncerTunnelProxyPath(syncTargetWorkspaceName logicalcluster.Name, syncTargetName, downstreamNamespaceName, podName, subresource, arguments string) (url.URL, error) {
	if syncTargetWorkspaceName.String() == "" || syncTargetName == "" || downstreamNamespaceName == "" || podName == "" || subresource == "" {
		return url.URL{}, fmt.Errorf("invalid tunnel path: workspaceName=%q, syncTargetName=%q, downstreamNamespaceName=%q, podName=%q, subresource=%q", syncTargetWorkspaceName.String(), syncTargetName, downstreamNamespaceName, podName, subresource)
	}
	proxyPath := defaultTunnelPathPrefix + fmt.Sprintf("/%s/apis/%s/synctargets/%s/%s/api/v1/namespaces/%s/pods/%s/%s", syncTargetWorkspaceName.String(), workloadv1alpha1.SchemeGroupVersion.String(), syncTargetName, cmdTunnelProxy, downstreamNamespaceName, podName, subresource)
	if arguments != "" {
		proxyPath += "?" + arguments
	}
	parse, err := url.Parse(proxyPath)
	if err != nil {
		return url.URL{}, err
	}
	return *parse, nil
}

// SyncerTunnelURL builds the destination url with the Dialer expected format of the URL.
func SyncerTunnelURL(host, ws, target string) (string, error) {
	if target == "" || ws == "" {
		return "", fmt.Errorf("target or ws can not be empty")
	}
	hostURL, err := url.Parse(host)
	if err != nil || hostURL.Scheme != "https" || hostURL.Host == "" {
		return "", fmt.Errorf("wrong url format, expected https://host<:port>/<path>: %w", err)
	}
	host = strings.Trim(host, "/")
	return host + defaultTunnelPathPrefix + "/" + ws + "/apis/" + workloadv1alpha1.SchemeGroupVersion.String() + "/synctargets/" + target, nil
}

// WithSyncerTunnel adds an HTTP Handler that handles reverse connections and reverse proxy requests using 2 different paths:
//
// https://host/services/syncer-tunnels/clusters/<ws>/apis/workload.kcp.io/v1alpha1/synctargets/<name>/connect establish reverse connections and queue them so it can be consumed by the dialer
// https://host/services/syncer-tunnels/clusters/<ws>/apis/workload.kcp.io/v1alpha1/synctargets/<name>/proxy/{path} proxies the {path} through the reverse connection identified by the cluster and syncer name
func WithSyncerTunnel(apiHandler http.Handler) http.HandlerFunc {
	pool := newTunnelPool()
	return func(w http.ResponseWriter, r *http.Request) {
		// fall through, syncer tunnels URL start by /services/tunnels
		if !strings.HasPrefix(r.URL.Path, defaultTunnelPathPrefix) {
			apiHandler.ServeHTTP(w, r)
			return
		}

		// route the request
		p := strings.TrimPrefix(r.URL.Path, defaultTunnelPathPrefix)
		path := strings.Split(strings.Trim(p, "/"), "/")
		if len(path) < 7 {
			http.Error(w, "invalid path", http.StatusInternalServerError)
			return
		}

		gv := workloadv1alpha1.SchemeGroupVersion
		if path[1] != "apis" ||
			path[2] != gv.Group ||
			path[3] != gv.Version ||
			path[4] != "synctargets" {
			http.Error(w, "invalid path", http.StatusInternalServerError)
			return
		}

		clusterName := logicalcluster.Name(path[0])
		syncerName := path[5]
		command := path[6]

		ctx := r.Context()
		logger := klog.FromContext(ctx)
		logger.V(5).Info("tunneler connection received", "command", command, "clusterName", clusterName, "syncerName", syncerName)
		switch command {
		case cmdTunnelConnect:
			if len(path) != 7 {
				http.Error(w, "syncer tunnels: invalid path for connect command", http.StatusInternalServerError)
				return
			}
			d := pool.getDialer(clusterName, syncerName)
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
				pool.deleteDialer(clusterName, syncerName)
				pool.createDialer(clusterName, syncerName, conn)
				// start control loop
				select {
				case <-r.Context().Done():
					conn.Close()
				case <-doneCh:
				}
				klog.Background().V(5).WithValues("cluster", clusterName, "syncer", syncerName).Info("stopped tunnel control connection")
				return
			}
			// create a reverse connection
			klog.Background().V(5).WithValues("cluster", clusterName, "syncer", syncerName).Info("tunnel started")
			select {
			case d.incomingConn <- conn:
			case <-d.Done():
				http.Error(w, "syncer tunnels: tunnel closed", http.StatusInternalServerError)
				return
			}
			// keep the handler alive until the connection is closed
			select {
			case <-r.Context().Done():
				conn.Close()
			case <-doneCh:
			}
			klog.Background().V(5).WithValues("address", r.RemoteAddr).Info("connection done")

		case cmdTunnelProxy:
			target, err := url.Parse("http://" + syncerName)
			if err != nil {
				http.Error(w, "wrong url", http.StatusInternalServerError)
				return
			}
			d := pool.getDialer(clusterName, syncerName)
			if d == nil || isClosedChan(d.Done()) {
				http.Error(w, "syncer tunnels: syncer not connected", http.StatusInternalServerError)
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
				if len(path) > 7 {
					proxypath += strings.Join(path[7:], "/")
				}
				req.URL.Path = proxypath
				// TODO: strip authorization header?????
				req.Header.Del("Authorization")
				director(req)
			}
			proxy.ServeHTTP(w, r)
			klog.Background().V(5).WithValues("err", err).Info("proxy server closed")
		default:
			http.Error(w, "syncer tunnels: unsupported command", http.StatusInternalServerError)
			return
		}
	}
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
