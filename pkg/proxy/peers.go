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
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// adminWorkspacePath is the path of the Admin workspace virtual workspace on
// every shard, serving the aggregated view of all Shard objects.
const adminWorkspacePath = "/services/admin"

// peerCooldown is how long a peer is skipped after a transport-level
// failure before it is tried again.
const peerCooldown = 30 * time.Second

// Peers is the failover set of shard endpoints serving the Admin workspace.
// It starts with the seed peers from the peer kubeconfigs and is extended at
// runtime with every discovered shard (UpsertShard/RemoveShard), so the
// discovery channel keeps working as long as any shard from the last
// observed state is reachable, even when every seed is gone. Seeds are
// permanent: they anchor bootstrapping and recovery from a fully stale
// dynamic set.
type Peers struct {
	cooldown time.Duration
	now      func() time.Time

	mu       sync.Mutex
	seeds    []*url.URL
	dynamic  map[string]*url.URL // shard name -> Admin workspace endpoint
	next     int
	failedAt map[string]time.Time // URL string -> last transport failure
}

func newPeers(seeds []*url.URL, cooldown time.Duration, now func() time.Time) *Peers {
	return &Peers{
		cooldown: cooldown,
		now:      now,
		seeds:    seeds,
		dynamic:  map[string]*url.URL{},
		failedAt: map[string]time.Time{},
	}
}

// UpsertShard adds or replaces the discovered shard's endpoint in the
// dynamic peer set. Endpoints that duplicate a seed are not tracked twice.
// Endpoints with a path cannot be failover targets - the round tripper only
// swaps scheme and host - and are rejected.
func (p *Peers) UpsertShard(name, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q for shard %q: %w", endpoint, name, err)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("endpoint %q for shard %q must not have a path", endpoint, name)
	}
	u.Path = ""

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, seed := range p.seeds {
		if seed.String() == u.String() {
			delete(p.dynamic, name)
			return nil
		}
	}
	p.dynamic[name] = u
	return nil
}

// RemoveShard drops the shard's endpoint from the dynamic peer set.
func (p *Peers) RemoveShard(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.dynamic[name]; ok {
		delete(p.failedAt, u.String())
	}
	delete(p.dynamic, name)
}

// pickOrder returns the current peers in round-robin order starting from
// the next slot, with peers inside the cooldown window moved to the back.
// Seeds come before dynamic peers in the underlying ring; the ring changes
// as shards come and go.
func (p *Peers) pickOrder() []*url.URL {
	p.mu.Lock()
	defer p.mu.Unlock()

	ring := make([]*url.URL, 0, len(p.seeds)+len(p.dynamic))
	ring = append(ring, p.seeds...)
	for _, name := range slices.Sorted(maps.Keys(p.dynamic)) {
		ring = append(ring, p.dynamic[name])
	}
	if len(ring) == 0 {
		return nil
	}

	start := p.next % len(ring)
	p.next = (start + 1) % len(ring)

	healthy := make([]*url.URL, 0, len(ring))
	var coolingDown []*url.URL
	for i := range ring {
		u := ring[(start+i)%len(ring)]
		if failed, ok := p.failedAt[u.String()]; ok && p.now().Sub(failed) < p.cooldown {
			coolingDown = append(coolingDown, u)
			continue
		}
		healthy = append(healthy, u)
	}
	return append(healthy, coolingDown...)
}

func (p *Peers) markFailed(u *url.URL) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failedAt[u.String()] = p.now()
}

func (p *Peers) markHealthy(u *url.URL) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.failedAt, u.String())
}

// NewPeersConfig loads one or more peer kubeconfigs and returns a
// rest.Config that talks to the Admin workspace (/services/admin) of the
// peer shards, distributing requests round-robin and failing over between
// them, together with the Peers set for extending the peers at runtime with
// discovered shards.
//
// Every named cluster across all kubeconfigs is a seed peer (duplicates by
// URL are collapsed); the first kubeconfig's current context supplies
// credentials and TLS settings, which must be valid for all peers. Because
// all peers serve the identical, cache-backed view in a single
// resourceVersion space, failover is exact: a watch broken by a peer outage
// can resume against another peer with the same resourceVersion.
func NewPeersConfig(kubeconfigPaths []string) (*rest.Config, *Peers, error) {
	var seeds []*url.URL
	seen := map[string]bool{}

	for _, kubeconfigPath := range kubeconfigPaths {
		raw, err := clientcmd.LoadFromFile(kubeconfigPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load shard peer kubeconfig %q: %w", kubeconfigPath, err)
		}

		names := make([]string, 0, len(raw.Clusters))
		for name := range raw.Clusters {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			server := raw.Clusters[name].Server
			u, err := url.Parse(server)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid server URL %q for peer %q in %q: %w", server, name, kubeconfigPath, err)
			}
			if u.Path != "" && u.Path != "/" {
				return nil, nil, fmt.Errorf("peer %q server URL %q in %q must not have a path", name, server, kubeconfigPath)
			}
			u.Path = ""
			if seen[u.String()] {
				continue
			}
			seen[u.String()] = true
			seeds = append(seeds, u)
		}
	}
	if len(seeds) == 0 {
		return nil, nil, fmt.Errorf("shard peer kubeconfigs %v contain no clusters", kubeconfigPaths)
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPaths[0]}, nil).ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build shard peer client config: %w", err)
	}

	peers := newPeers(seeds, peerCooldown, time.Now)

	first := *seeds[0]
	first.Path = path.Join(first.Path, adminWorkspacePath)
	config.Host = first.String()
	config.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &peerFailoverRoundTripper{delegate: rt, peers: peers}
	})
	return config, peers, nil
}

// peerFailoverRoundTripper distributes requests round-robin across the
// peers, skipping peers that failed within the cooldown window, and
// advancing to the next peer on transport-level errors. Only body-less
// requests (informer GETs, WATCHes) are retried against further peers;
// requests with a body get a single attempt.
type peerFailoverRoundTripper struct {
	delegate http.RoundTripper
	peers    *Peers
}

func (rt *peerFailoverRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	order := rt.peers.pickOrder()
	if len(order) == 0 {
		return nil, fmt.Errorf("no shard peers available")
	}
	if req.Body != nil && req.Body != http.NoBody {
		order = order[:1]
	}

	var lastErr error
	for _, u := range order {
		r := req.Clone(req.Context())
		r.URL.Scheme = u.Scheme
		r.URL.Host = u.Host
		r.Host = ""

		resp, err := rt.delegate.RoundTrip(r)
		if err == nil {
			rt.peers.markHealthy(u)
			return resp, nil
		}
		rt.peers.markFailed(u)
		lastErr = err
		if req.Context().Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("all %d attempted shard peers failed, last error: %w", len(order), lastErr)
}
