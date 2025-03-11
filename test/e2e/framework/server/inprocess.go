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

package server

import (
	"context"
	"testing"

	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/component-base/cli/flag"

	kcpoptions "github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/server"
)

func init() {
	RunInProcessFunc = func(t *testing.T, rootDir string, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		serverOptions := kcpoptions.NewOptions(rootDir)
		fss := flag.NamedFlagSets{}
		serverOptions.AddFlags(&fss)
		all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
		for _, fs := range fss.FlagSets {
			all.AddFlagSet(fs)
		}
		if err := all.Parse(args); err != nil {
			return err
		}

		completed, err := serverOptions.Complete()
		if err != nil {
			return err
		}
		if errs := completed.Validate(); len(errs) > 0 {
			return utilerrors.NewAggregate(errs)
		}

		config, err := server.NewConfig(ctx, completed.Server)
		if err != nil {
			return err
		}

		completedConfig, err := config.Complete()
		if err != nil {
			return err
		}

		// the etcd server must be up before NewServer because storage decorators access it right away
		if completedConfig.EmbeddedEtcd.Config != nil {
			if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
				return err
			}
		}

		s, err := server.NewServer(completedConfig)
		if err != nil {
			return err
		}
		go func() {
			if err := s.Run(ctx); err != nil && ctx.Err() == nil {
				t.Errorf("`kcp` failed: %v", err)
			}
		}()

		return nil
	}
}
