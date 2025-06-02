/*
Copyright 2025 The KCP Authors.

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

	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli/flag"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/embeddedetcd"

	kcpoptions "github.com/kcp-dev/kcp/cmd/kcp/options"
	kcpserver "github.com/kcp-dev/kcp/pkg/server"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
)

var ContextRunInProcess kcptestingserver.KcpRunner = func(ctx context.Context, t kcptestingserver.TestingT, cfg kcptestingserver.Config) (<-chan struct{}, error) {
	s := NewInProcessServer(t)
	s.Config = cfg
	s.Start(ctx, t)
	s.Wait(t)
	return s.StopCh, nil
}

func init() {
	kcptestingserver.ContextRunInProcessFunc = ContextRunInProcess
}

type InProcessServer struct {
	Config kcptestingserver.Config

	Server *kcpserver.Server
	StopCh chan struct{}

	cancelOnce sync.Once
	cancel     context.CancelFunc

	loadCfgOnce  sync.Once
	ClientConfig clientcmd.ClientConfig
}

func NewInProcessServer(t kcptestingserver.TestingT, opts ...kcptestingserver.Option) *InProcessServer {
	t.Helper()

	opts = append(
		[]kcptestingserver.Option{
			kcptestingserver.WithDefaultsFrom(t),
			kcptestingserver.WithRunInProcess(),
			kcptestingserver.WithBindAddress("127.0.0.1"),
		},
		opts...,
	)

	cfg := &kcptestingserver.Config{}
	for _, opt := range opts {
		opt(cfg)
	}

	return &InProcessServer{
		Config: *cfg,
	}
}

var globalOptionsLock sync.Mutex

func (s *InProcessServer) Start(ctx context.Context, t kcptestingserver.TestingT) {
	t.Helper()

	ctx, s.cancel = context.WithCancel(ctx)
	t.Cleanup(s.cancel)

	// During the call of kcpoptions.NewOptions global values (e.g.
	// feature gates) are initialized. This can produce data races and
	// panics if tests are run in parallel.
	globalOptionsLock.Lock()
	serverOptions := kcpoptions.NewOptions(s.Config.DataDir)
	globalOptionsLock.Unlock()

	fss := flag.NamedFlagSets{}
	serverOptions.AddFlags(&fss)
	all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
	for _, fs := range fss.FlagSets {
		all.AddFlagSet(fs)
	}

	args, err := s.Config.BuildArgs(t)
	if err != nil {
		t.Fatalf("failed to build args: %v", err)
	}

	if err := all.Parse(args); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	completed, err := serverOptions.Complete(ctx)
	if err != nil {
		t.Fatalf("failed to complete server options: %v", err)
	}
	if errs := completed.Validate(); len(errs) > 0 {
		t.Fatalf("failed to validate server options: %v", utilerrors.NewAggregate(errs))
	}

	config, err := kcpserver.NewConfig(ctx, completed.Server)
	if err != nil {
		t.Fatalf("failed to create server config: %v", err)
	}

	completedConfig, err := config.Complete()
	if err != nil {
		t.Fatalf("failed to complete server config: %v", err)
	}

	etcdCtx, etcdCancel := context.WithCancel(context.Background())

	// the etcd server must be up before NewServer because storage decorators access it right away
	if completedConfig.EmbeddedEtcd.Config != nil {
		if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(etcdCtx); err != nil {
			etcdCancel()
			t.Fatalf("failed to start embedded etcd: %v", err)
		}
	}

	s.StopCh = make(chan struct{})
	s.Server, err = kcpserver.NewServer(completedConfig)
	if err != nil {
		etcdCancel()
		t.Fatalf("failed to create kcp server: %v", err)
	}
	go func() {
		defer close(s.StopCh)
		defer etcdCancel()
		if err := s.Server.Run(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("`kcp` failed: %v", err)
		}
	}()
}

func (s *InProcessServer) Wait(t kcptestingserver.TestingT) {
	// TODO: replace with t.Context() in go1.24
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := kcptestingserver.WaitForReady(ctx, s.RESTConfig(t, "base")); err != nil {
		t.Fatalf("server did not become ready: %v", err)
	}
}

func (s *InProcessServer) Stop() {
	s.cancelOnce.Do(func() {
		s.cancel()
		s.cancel = nil
	})
	<-s.StopCh
}

func (s *InProcessServer) loadCfg(t kcptestingserver.TestingT) {
	t.Helper()
	s.loadCfgOnce.Do(func() {
		// TODO replace with t.Context() in go1.24
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		config, err := kcptestingserver.WaitLoadKubeConfig(ctx, s.Config.KubeconfigPath(), "base")
		if err != nil {
			t.Fatalf("failed to load base kubeconfig: %v", err)
		}
		s.ClientConfig = config
	})
}

func (s *InProcessServer) RESTConfig(t kcptestingserver.TestingT, context string) *rest.Config {
	t.Helper()
	s.loadCfg(t)
	raw, err := s.ClientConfig.RawConfig()
	if err != nil {
		t.Fatalf("failed to get raw config: %v", err)
	}

	restConfig, err := clientcmd.NewNonInteractiveClientConfig(raw, context, nil, nil).ClientConfig()
	if err != nil {
		t.Fatalf("failed to get client config for context %q: %v", context, err)
	}

	restConfig.QPS = -1

	return restConfig
}

// StartTestServer starts a KCP server for testing purposes.
func StartTestServer(t kcptestingserver.TestingT, opts ...kcptestingserver.Option) (*InProcessServer, kcpclientset.ClusterInterface, kcpkubernetesclientset.ClusterInterface) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := NewInProcessServer(t, opts...)
	s.Start(ctx, t)
	s.Wait(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(s.RESTConfig(t, "base"))
	if err != nil {
		t.Fatal(err)
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(s.RESTConfig(t, "base"))
	if err != nil {
		t.Fatal(err)
	}

	return s, kcpClusterClient, kubeClusterClient
}
