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
	"net/url"
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	tunnelSubresourcePath = "tunnel"
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

// syncerTunnelURL builds the destination url with the Dialer expected format of the URL.
func SyncerTunnelURL(host, ws, target string) (string, error) {
	if target == "" || ws == "" {
		return "", fmt.Errorf("target or ws can not be empty")
	}
	hostURL, err := url.Parse(host)
	if err != nil || hostURL.Scheme != "https" || hostURL.Host == "" {
		return "", fmt.Errorf("wrong url format, expected https://host<:port>/<path>: %w", err)
	}
	return url.JoinPath(hostURL.String(), "clusters", ws, "apis", workloadv1alpha1.SchemeGroupVersion.String(), "synctargets", target)
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
