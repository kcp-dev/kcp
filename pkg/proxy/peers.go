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
	"net/http"
	"net/url"
	"path"
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

// NewPeersConfig loads one or more peer kubeconfigs and returns a
// rest.Config that talks to the Admin workspace (/services/admin) of the
// peer shards, distributing requests round-robin and failing over between
// them.
//
// Every named cluster across all kubeconfigs is a peer (duplicates by URL
// are collapsed); the first kubeconfig's current context supplies
// credentials and TLS settings, which must be valid for all peers. Because
// all peers serve the identical, cache-backed view in a single
// resourceVersion space, failover is exact: a watch broken by a peer outage
// can resume against another peer with the same resourceVersion.
func NewPeersConfig(kubeconfigPaths []string) (*rest.Config, error) {
	var peers []*url.URL
	seen := map[string]bool{}

	for _, kubeconfigPath := range kubeconfigPaths {
		raw, err := clientcmd.LoadFromFile(kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load shard peer kubeconfig %q: %w", kubeconfigPath, err)
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
				return nil, fmt.Errorf("invalid server URL %q for peer %q in %q: %w", server, name, kubeconfigPath, err)
			}
			if u.Path != "" && u.Path != "/" {
				return nil, fmt.Errorf("peer %q server URL %q in %q must not have a path", name, server, kubeconfigPath)
			}
			if seen[u.String()] {
				continue
			}
			seen[u.String()] = true
			peers = append(peers, u)
		}
	}
	if len(peers) == 0 {
		return nil, fmt.Errorf("shard peer kubeconfigs %v contain no clusters", kubeconfigPaths)
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPaths[0]}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build shard peer client config: %w", err)
	}

	first := *peers[0]
	first.Path = path.Join(first.Path, adminWorkspacePath)
	config.Host = first.String()
	config.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &peerFailoverRoundTripper{delegate: rt, peers: peers, cooldown: peerCooldown, now: time.Now}
	})
	return config, nil
}

// peerFailoverRoundTripper distributes requests round-robin across the
// peers, skipping peers that failed within the cooldown window, and
// advancing to the next peer on transport-level errors. Only body-less
// requests (informer GETs, WATCHes) are retried against further peers;
// requests with a body get a single attempt.
type peerFailoverRoundTripper struct {
	delegate http.RoundTripper
	peers    []*url.URL
	cooldown time.Duration
	now      func() time.Time

	mu       sync.Mutex
	next     int
	failedAt map[int]time.Time
}

// pickOrder returns peer indices in round-robin order starting from the
// next slot, with peers inside the cooldown window moved to the back.
func (rt *peerFailoverRoundTripper) pickOrder() []int {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	start := rt.next
	rt.next = (rt.next + 1) % len(rt.peers)

	healthy := make([]int, 0, len(rt.peers))
	var coolingDown []int
	for i := range rt.peers {
		idx := (start + i) % len(rt.peers)
		if failed, ok := rt.failedAt[idx]; ok && rt.now().Sub(failed) < rt.cooldown {
			coolingDown = append(coolingDown, idx)
			continue
		}
		healthy = append(healthy, idx)
	}
	return append(healthy, coolingDown...)
}

func (rt *peerFailoverRoundTripper) markFailed(idx int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.failedAt == nil {
		rt.failedAt = map[int]time.Time{}
	}
	rt.failedAt[idx] = rt.now()
}

func (rt *peerFailoverRoundTripper) markHealthy(idx int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.failedAt, idx)
}

func (rt *peerFailoverRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	order := rt.pickOrder()
	if req.Body != nil && req.Body != http.NoBody {
		order = order[:1]
	}

	var lastErr error
	for _, idx := range order {
		r := req.Clone(req.Context())
		r.URL.Scheme = rt.peers[idx].Scheme
		r.URL.Host = rt.peers[idx].Host
		r.Host = ""

		resp, err := rt.delegate.RoundTrip(r)
		if err == nil {
			rt.markHealthy(idx)
			return resp, nil
		}
		rt.markFailed(idx)
		lastErr = err
		if req.Context().Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("all %d attempted shard peers failed, last error: %w", len(order), lastErr)
}
