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

package framework

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// KCPFixture manages the lifecycle of a set of kcp servers.
type KCPFixture struct {
	Servers map[string]RunningServer

	configs []KcpConfig
}

func NewKCPFixture(cfgs ...KcpConfig) *KCPFixture {
	return &KCPFixture{
		configs: cfgs,
	}
}

func (f *KCPFixture) SetUp(t *testing.T) {
	artifactDir, dataDir, err := ScratchDirs(t)
	require.NoErrorf(t, err, "failed to create scratch dirs: %v", err)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// Initialize servers from the provided configuration
	var servers []*kcpServer
	f.Servers = map[string]RunningServer{}
	for _, cfg := range f.configs {
		server, err := newKcpServer(NewT(ctx, t), cfg, artifactDir, dataDir)
		require.NoError(t, err)

		servers = append(servers, server)
		f.Servers[server.name] = server
	}

	// Launch kcp servers and ensure they are ready before starting the test
	start := time.Now()
	t.Log("Starting kcp servers...")
	wg := sync.WaitGroup{}
	wg.Add(len(servers))
	for _, srv := range servers {
		err := srv.Run(ctx)
		require.NoError(t, err)

		// Wait for the server to become ready
		go func(s *kcpServer) {
			defer wg.Done()
			err := s.Ready()
			require.NoErrorf(t, err, "kcp server %s never became ready: %v", s.name, err)
		}(srv)
	}
	wg.Wait()

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Logf("Started kcp servers after %s", time.Since(start))
}
