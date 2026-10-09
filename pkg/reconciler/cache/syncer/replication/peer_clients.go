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

package replication

import (
	"sync"

	"k8s.io/client-go/rest"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	clientshard "github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

// buildPeerConfig returns a *rest.Config for a peer cache-server, applying the
// three cache round-trippers required for shard-in-URL routing.
func buildPeerConfig(host string, tlsConfig rest.TLSClientConfig) *rest.Config {
	cfg := &rest.Config{
		Host:            host,
		TLSClientConfig: tlsConfig,
	}
	cfg = cacheclient.WithCacheServiceRoundTripper(cfg)
	cfg = cacheclient.WithShardNameFromContextRoundTripper(cfg)
	cfg = cacheclient.WithDefaultShardRoundTripper(cfg, clientshard.Wildcard)
	return cfg
}

// PeerClientMap is a thread-safe map from peer name (Cache object name) to a
// REST config for that peer. Clients are constructed once per peer and reused
// across all GVRControllers. Add is idempotent.
type PeerClientMap struct {
	mu      sync.RWMutex
	clients map[string]*rest.Config
}

func newPeerClientMap() *PeerClientMap {
	return &PeerClientMap{
		clients: make(map[string]*rest.Config),
	}
}

// Add inserts or replaces the config for the given peer name.
func (m *PeerClientMap) Add(name string, cfg *rest.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[name] = cfg
}

// Get returns the config for the given peer name and whether it exists.
func (m *PeerClientMap) Get(name string) (*rest.Config, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.clients[name]
	return cfg, ok
}

// Delete removes the given peer from the map.
func (m *PeerClientMap) Delete(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, name)
}

// Names returns a snapshot of the current peer names.
func (m *PeerClientMap) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}
