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
	"k8s.io/component-base/cli/flag"

	"github.com/kcp-dev/embeddedetcd"

	kcpoptions "github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/server"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
)

func init() {
	globalOptionsLock := &sync.Mutex{}

	kcptestingserver.ContextRunInProcessFunc = func(ctx context.Context, t kcptestingserver.TestingT, cfg kcptestingserver.Config) (<-chan struct{}, error) {
		ctx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)

		// During the setups global values (e.g. feature gates) are
		// initialized. This can produce data races as well as panics.
		globalOptionsLock.Lock()
		serverOptions := kcpoptions.NewOptions(cfg.DataDir)
		globalOptionsLock.Unlock()

		fss := flag.NamedFlagSets{}
		serverOptions.AddFlags(&fss)
		all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
		for _, fs := range fss.FlagSets {
			all.AddFlagSet(fs)
		}
		if err := all.Parse(cfg.Args); err != nil {
			return nil, err
		}

		completed, err := serverOptions.Complete(ctx)
		if err != nil {
			return nil, err
		}
		if errs := completed.Validate(); len(errs) > 0 {
			return nil, utilerrors.NewAggregate(errs)
		}

		config, err := server.NewConfig(ctx, completed.Server)
		if err != nil {
			return nil, err
		}

		completedConfig, err := config.Complete()
		if err != nil {
			return nil, err
		}

		// the etcd server must be up before NewServer because storage decorators access it right away
		if completedConfig.EmbeddedEtcd.Config != nil {
			if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
				return nil, err
			}
		}

		stopCh := make(chan struct{})
		s, err := server.NewServer(completedConfig)
		if err != nil {
			return nil, err
		}
		go func() {
			defer close(stopCh)
			if err := s.Run(ctx); err != nil && ctx.Err() == nil {
				t.Errorf("`kcp` failed: %v", err)
			}
		}()

		return stopCh, nil
	}
}
