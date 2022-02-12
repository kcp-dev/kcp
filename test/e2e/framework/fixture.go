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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

// KcpFixture manages the lifecycle of a set of kcp servers.
type KcpFixture struct {
	Servers map[string]RunningServer
}

func NewKcpFixture(t *testing.T, cfgs ...KcpConfig) *KcpFixture {
	f := &KcpFixture{}

	artifactDir, dataDir, err := ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs: %v", err)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// Initialize servers from the provided configuration
	var servers []*kcpServer
	f.Servers = map[string]RunningServer{}
	for _, cfg := range cfgs {
		if !TestConfig.InProcessControllers {
			// If not running in-process-controllers, arguments for
			// each controller must be aggregated with the server's
			// arguments to achieve the desired configuration.
			for _, f := range cfg.Controllers {
				cfg.Args = append(cfg.Args, f.GetServerArgs()...)
			}
		}
		server, err := newKcpServer(t, cfg, artifactDir, dataDir)
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
			require.NoError(t, err, "kcp server %s never became ready: %v", s.name, err)
		}(srv)
	}
	wg.Wait()

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Logf("Started kcp servers after %s", time.Since(start))

	if TestConfig.InProcessControllers {
		for _, cfg := range cfgs {
			for _, ctlFixture := range cfg.Controllers {
				t.Logf("Starting in-process %q for server %q", ctlFixture.GetName(), cfg.Name)
				ctlFixture.Run(t, f.Servers[cfg.Name])
			}
		}
	}

	return f
}

type ControllerFixture interface {
	// GetName will return the name of the controller. This can be
	// used in cases where the fixture needs to be identified for
	// logging purposes.
	GetName() string
	// GetServerArgs returns the cli arguments required to configure
	// kcp to run the controller. This will be called when configuring
	// an out-of-process kcp server managed by the test run.
	GetServerArgs() []string
	// Configure enables the controller fixture to run itself against
	// the given server. This will be called when running the
	// controller in-process.
	Run(t *testing.T, server RunningServer)
}

type ClusterControllerFixture struct {
	Args             []string
	OutOfProcessArgs []string
}

func (f *ClusterControllerFixture) GetName() string {
	return "ClusterController"
}

func (f *ClusterControllerFixture) GetServerArgs() []string {
	return append(f.Args, f.OutOfProcessArgs...)
}

func (f *ClusterControllerFixture) Run(t *testing.T, server RunningServer) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	f.Args = append(f.Args, fmt.Sprintf("--kubeconfig=%s", server.KubeconfigPath()))

	fs := pflag.NewFlagSet("cluster-controller", pflag.ContinueOnError)
	options := cluster.BindCmdOptions(fs)
	err := fs.Parse(f.Args)
	require.NoError(t, err)
	err = options.Validate()
	require.NoError(t, err)

	err = cluster.StartController(ctx, options)
	require.NoError(t, err)
}
