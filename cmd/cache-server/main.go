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

package main

import (
	"flag"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/component-base/cli"

	cacheserver "github.com/kcp-dev/kcp/pkg/cache/server"
	"github.com/kcp-dev/kcp/pkg/cache/server/options"
	"github.com/kcp-dev/kcp/pkg/cmd/help"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
)

func main() {
	rand.Seed(time.Now().UTC().UnixNano())

	rootDir := flag.String("root-directory", ".kcp-cache", "Path to the root directory where all files required by this server will be stored")

	var cacheServerFlags, remainingFlags []string //nolint:prealloc
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--root-directory") {
			cacheServerFlags = append(cacheServerFlags, arg)
			continue
		}
		remainingFlags = append(remainingFlags, arg)
	}
	flag.CommandLine.Parse(cacheServerFlags) //nolint:errcheck
	os.Args = append([]string{os.Args[0]}, remainingFlags...)

	serverOptions := options.NewOptions(*rootDir)
	cmd := &cobra.Command{
		Use:   "cache-server",
		Short: "Runs the cache server for KCP",
		Long: help.Doc(`
            Starts a server that hosts data/resources that are required by shards.
            It serves as a cache helping to reduce the storage that would have to
            be copied onto every shard otherwise.

            The actual group of shards that will use this server should be part of
            the topology. For example, it can be used only by shards that are in
            the same geographical region.

            On a high level, the server exposes two HTTP paths. The first one is
            used by the shards for getting all resources. The second one is used
            by individual shards to push data they wish to be shared.

            There are no limits on the types of data this server hosts. The rule of
            thumb is that they must be common for a larger group of shards.
            For example the root APIs.
		`),

		RunE: func(c *cobra.Command, args []string) error {
			completed, err := serverOptions.Complete()
			if err != nil {
				return err
			}
			if errs := completed.Validate(); len(errs) > 0 {
				return errors.NewAggregate(errs)
			}

			config, err := cacheserver.NewConfig(completed, nil)
			if err != nil {
				return err
			}
			completedConfig, err := config.Complete()
			if err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()
			// the etcd server must be up before NewServer because storage decorators access it right away
			if completedConfig.EmbeddedEtcd.Config != nil {
				if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
					return err
				}
			}

			server, err := cacheserver.NewServer(completedConfig)
			if err != nil {
				return err
			}
			prepared, err := server.PrepareRun(ctx)
			if err != nil {
				return err
			}
			return prepared.Run(ctx)
		},
	}

	serverOptions.AddFlags(cmd.Flags())
	code := cli.Run(cmd)
	os.Exit(code)
}
