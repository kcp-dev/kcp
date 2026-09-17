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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

func testPeers(t *testing.T, seeds ...string) *Peers {
	t.Helper()
	urls := make([]*url.URL, 0, len(seeds))
	for _, s := range seeds {
		u, err := url.Parse(s)
		require.NoError(t, err)
		urls = append(urls, u)
	}
	return newPeers(urls, peerCooldown, time.Now)
}

// roundTrip sends a body-less GET through rt and returns the host that
// served it.
func roundTrip(t *testing.T, rt http.RoundTripper) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://placeholder/services/admin/apis/core.kcp.io/v1alpha1/shards", http.NoBody)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return resp.Request.URL.Host, nil
}

func writeKubeconfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestPeerFailoverAdvancesOnConnectionError(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"a:6443": true}}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}

	host, err := roundTrip(t, rt)
	require.NoError(t, err)
	require.Equal(t, "b:6443", host, "expected failover to b:6443")

	// the next request must go straight to the healthy peer.
	transport.seen = nil
	_, err = roundTrip(t, rt)
	require.NoError(t, err)
	require.Equal(t, []string{"b:6443"}, transport.seen, "expected a single attempt against b:6443")
}

func TestPeerFailoverAllPeersDown(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"a:6443": true, "b:6443": true}}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}
	_, err := roundTrip(t, rt)
	require.ErrorContains(t, err, "all 2 attempted shard peers failed")
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
	kubeconfigPath := writeKubeconfig(t, t.TempDir(), "peers.kubeconfig", peersKubeconfig)

	config, _, err := NewPeersConfig([]string{kubeconfigPath})
	require.NoError(t, err)
	// peers are sorted by cluster name; the Host points the client at the
	// Admin workspace of the first one.
	require.Equal(t, "https://a:6444/services/admin", config.Host)
}

func TestNewPeersConfigRejectsServerWithPath(t *testing.T) {
	t.Parallel()
	bad := strings.Replace(peersKubeconfig, "https://a:6444", "https://a:6444/base", 1)
	kubeconfigPath := writeKubeconfig(t, t.TempDir(), "peers.kubeconfig", bad)

	_, _, err := NewPeersConfig([]string{kubeconfigPath})
	require.Error(t, err, "expected an error for a peer server URL with a path")
}

func TestNewPeersConfigMergesMultipleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := writeKubeconfig(t, dir, "one.kubeconfig", peersKubeconfig)
	more := strings.ReplaceAll(peersKubeconfig, "https://a:6444", "https://c:6446")
	more = strings.ReplaceAll(more, "https://b:6445", "https://a:6444") // duplicate of first file
	second := writeKubeconfig(t, dir, "two.kubeconfig", more)

	config, peers, err := NewPeersConfig([]string{first, second})
	require.NoError(t, err)
	require.Equal(t, "https://a:6444/services/admin", config.Host)
	require.Len(t, peers.pickOrder(), 3, "expected duplicate seeds to be collapsed")
}

func TestPeerRoundRobinDistribution(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{}
	rt := &peerFailoverRoundTripper{
		delegate: transport,
		peers:    testPeers(t, "https://a:6443", "https://b:6443"),
	}
	for range 4 {
		_, err := roundTrip(t, rt)
		require.NoError(t, err)
	}
	require.Equal(t, []string{"a:6443", "b:6443", "a:6443", "b:6443"}, transport.seen, "expected round-robin distribution")
}

func TestPeersSeedsAreNotUsedOnceShardsAreDiscovered(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-1", "https://shard-1:6443"))
	require.NoError(t, peers.UpsertShard("shard-2", "https://shard-2:6443"))

	for range 4 {
		_, err := roundTrip(t, rt)
		require.NoError(t, err)
	}
	require.Equal(t, []string{"shard-1:6443", "shard-2:6443", "shard-1:6443", "shard-2:6443"}, transport.seen,
		"expected round-robin across discovered shards only, never the seed")
}

func TestPeersSeedsAreFallbackWhenAllShardsFail(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"shard-1:6443": true, "shard-2:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-1", "https://shard-1:6443"))
	require.NoError(t, peers.UpsertShard("shard-2", "https://shard-2:6443"))

	host, err := roundTrip(t, rt)
	require.NoError(t, err)
	require.Equal(t, "seed:6443", host, "expected the seed to serve when every discovered shard is down")
	require.Equal(t, []string{"shard-1:6443", "shard-2:6443", "seed:6443"}, transport.seen)
}

func TestPeersStaleSeedIsNotAttempted(t *testing.T) {
	t.Parallel()
	// the seed shard moved to a new endpoint; the old seed URL is dead.
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-0", "https://moved:6443"))

	for range 3 {
		host, err := roundTrip(t, rt)
		require.NoError(t, err)
		require.Equal(t, "moved:6443", host)
	}
	require.NotContains(t, transport.seen, "seed:6443", "a stale seed must not receive traffic while discovered shards are healthy")
}

func TestPeersDynamicShardBecomesFailoverTarget(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-1", "https://shard-1:6443"))

	host, err := roundTrip(t, rt)
	require.NoError(t, err)
	require.Equal(t, "shard-1:6443", host, "expected the discovered shard to serve")
}

func TestPeersRemoveShardDropsFailoverTarget(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true, "shard-1:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-1", "https://shard-1:6443"))
	peers.RemoveShard("shard-1")

	_, err := roundTrip(t, rt)
	require.Error(t, err, "expected an error with the seed down and the shard removed")
	require.NotContains(t, transport.seen, "shard-1:6443", "removed shard must not be attempted")
}

func TestPeersUpsertShardRejectsPathAndDuplicatesSeed(t *testing.T) {
	t.Parallel()
	peers := testPeers(t, "https://seed:6443")

	require.Error(t, peers.UpsertShard("bad", "https://shard:6443/base"), "expected an error for an endpoint with a path")

	// a shard whose endpoint equals a seed must not be attempted twice.
	require.NoError(t, peers.UpsertShard("seed-twin", "https://seed:6443"))
	require.Len(t, peers.pickOrder(), 1)
}

func TestPeersUpsertShardReplacesEndpoint(t *testing.T) {
	t.Parallel()
	transport := &fakeTransport{downHosts: map[string]bool{"seed:6443": true, "old:6443": true}}
	peers := testPeers(t, "https://seed:6443")
	rt := &peerFailoverRoundTripper{delegate: transport, peers: peers}

	require.NoError(t, peers.UpsertShard("shard-1", "https://old:6443"))
	require.NoError(t, peers.UpsertShard("shard-1", "https://new:6443"))

	host, err := roundTrip(t, rt)
	require.NoError(t, err)
	require.Equal(t, "new:6443", host, "expected the replaced endpoint to serve")
	require.NotContains(t, transport.seen, "old:6443", "replaced endpoint must not be attempted")
}

func TestPeersForgetFailuresOfUnusedEndpoints(t *testing.T) {
	t.Parallel()
	peers := testPeers(t, "https://seed:6443")
	old, err := url.Parse("https://old:6443")
	require.NoError(t, err)
	seed, err := url.Parse("https://seed:6443")
	require.NoError(t, err)

	require.NoError(t, peers.UpsertShard("shard-1", old.String()))
	require.NoError(t, peers.UpsertShard("shard-2", seed.String()))
	peers.markFailed(old)
	peers.markFailed(seed)

	require.NoError(t, peers.UpsertShard("shard-1", "https://new:6443"))
	peers.RemoveShard("shard-2")

	require.NotContains(t, peers.failedAt, old.String(), "failure of a replaced endpoint must be forgotten")
	require.Contains(t, peers.failedAt, seed.String(), "failure of an endpoint still used by a seed must be kept")
}
