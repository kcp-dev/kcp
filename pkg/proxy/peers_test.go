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

package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeTransport struct {
	downHosts map[string]bool
	seen      []string
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, req.URL.Host)
	if f.downHosts[req.URL.Host] {
		return nil, errors.New("connection refused")
	}
	return &http.Response{StatusCode: http.StatusOK, Request: req, Body: http.NoBody}, nil
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func testPeers(t *testing.T, seeds ...string) *Peers {
	t.Helper()
	urls := make([]*url.URL, 0, len(seeds))
	for _, s := range seeds {
		urls = append(urls, mustURL(t, s))
	}
	return newPeers(urls, peerCooldown, time.Now)
}

func TestPeerFailoverAdvancesOnConnectionError(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"a:6443": true}}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/services/admin/apis/core.kcp.io/v1alpha1/shards", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Host != "b:6443" {
		t.Errorf("expected failover to b:6443, got %q", resp.Request.URL.Host)
	}

	// the next request must go straight to the healthy peer.
	transport.seen = nil
	resp2, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if len(transport.seen) != 1 || transport.seen[0] != "b:6443" {
		t.Errorf("expected a single attempt against b:6443, got %v", transport.seen)
	}
}

func TestPeerFailoverAllPeersDown(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"a:6443": true, "b:6443": true}}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/x", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an error when all peers are down")
	} else if !strings.Contains(err.Error(), "all 2 attempted shard peers failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

const peersKubeconfig = `
apiVersion: v1
kind: Config
clusters:
- name: shard-b
  cluster:
    server: https://b:6445
    insecure-skip-tls-verify: true
- name: shard-a
  cluster:
    server: https://a:6444
    insecure-skip-tls-verify: true
contexts:
- name: peers
  context:
    cluster: shard-a
    user: admin
users:
- name: admin
  user:
    token: abc
current-context: peers
`

func TestNewPeersConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	kubeconfigPath := filepath.Join(dir, "peers.kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(peersKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	config, _, err := NewPeersConfig([]string{kubeconfigPath})
	if err != nil {
		t.Fatal(err)
	}
	// peers are sorted by cluster name; the Host points the client at the
	// Admin workspace of the first one.
	if config.Host != "https://a:6444/services/admin" {
		t.Errorf("unexpected host %q", config.Host)
	}
}

func TestNewPeersConfigRejectsServerWithPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	kubeconfigPath := filepath.Join(dir, "peers.kubeconfig")
	bad := strings.Replace(peersKubeconfig, "https://a:6444", "https://a:6444/base", 1)
	if err := os.WriteFile(kubeconfigPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewPeersConfig([]string{kubeconfigPath}); err == nil {
		t.Fatal("expected an error for a peer server URL with a path")
	}
}

func TestPeerRoundRobinDistribution(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}
	for range 4 {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/x", http.NoBody)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	want := []string{"a:6443", "b:6443", "a:6443", "b:6443"}
	if !slices.Equal(transport.seen, want) {
		t.Errorf("expected round-robin distribution %v, got %v", want, transport.seen)
	}
}

func TestNewPeersConfigMergesMultipleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := filepath.Join(dir, "one.kubeconfig")
	if err := os.WriteFile(first, []byte(peersKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "two.kubeconfig")
	more := strings.ReplaceAll(peersKubeconfig, "https://a:6444", "https://c:6446")
	more = strings.ReplaceAll(more, "https://b:6445", "https://a:6444") // duplicate of first file
	if err := os.WriteFile(second, []byte(more), 0o600); err != nil {
		t.Fatal(err)
	}

	config, _, err := NewPeersConfig([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://a:6444/services/admin" {
		t.Errorf("unexpected host %q", config.Host)
	}
}

func TestPeersDynamicShardBecomesFailoverTarget(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	if err := peers.UpsertShard("shard-1", "https://shard-1:6443"); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/x", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Host != "shard-1:6443" {
		t.Errorf("expected failover to the discovered shard, got %q", resp.Request.URL.Host)
	}
}

func TestPeersRemoveShardDropsFailoverTarget(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true, "shard-1:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	if err := peers.UpsertShard("shard-1", "https://shard-1:6443"); err != nil {
		t.Fatal(err)
	}
	peers.RemoveShard("shard-1")

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/x", http.NoBody)
	if resp, err := rt.RoundTrip(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected an error with the seed down and the shard removed")
	}
	for _, host := range transport.seen {
		if host == "shard-1:6443" {
			t.Errorf("removed shard must not be attempted, attempts: %v", transport.seen)
		}
	}
}

func TestPeersUpsertShardRejectsPathAndDuplicatesSeed(t *testing.T) {
	t.Parallel()
	peers := testPeers(t, "https://seed:6443")

	if err := peers.UpsertShard("bad", "https://shard:6443/base"); err == nil {
		t.Error("expected an error for an endpoint with a path")
	}

	// a shard whose endpoint equals a seed must not create a second ring slot.
	if err := peers.UpsertShard("seed-twin", "https://seed:6443"); err != nil {
		t.Fatal(err)
	}
	if got := len(peers.pickOrder()); got != 1 {
		t.Errorf("expected a single peer in the ring, got %d", got)
	}
}

func TestPeersUpsertShardReplacesEndpoint(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true, "old:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	if err := peers.UpsertShard("shard-1", "https://old:6443"); err != nil {
		t.Fatal(err)
	}
	if err := peers.UpsertShard("shard-1", "https://new:6443"); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/x", http.NoBody)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Host != "new:6443" {
		t.Errorf("expected the replaced endpoint to serve, got %q", resp.Request.URL.Host)
	}
	for _, host := range transport.seen {
		if host == "old:6443" {
			t.Errorf("replaced endpoint must not be attempted, attempts: %v", transport.seen)
		}
	}
}
