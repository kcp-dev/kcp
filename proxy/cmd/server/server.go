/*
Copyright 2021 The KCP Authors.

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
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/proxy/cmd/help"
	clientoptions "github.com/kcp-dev/kcp/proxy/cmd/server/options"
	"github.com/kcp-dev/kcp/proxy/manager"
	"github.com/kcp-dev/kcp/proxy/manager/options"
)

func main() {
	rand.Seed(time.Now().UTC().UnixNano())

	cmd := &cobra.Command{
		Use:   "server",
		Short: "WPS Workspace proxy Server - kcp sub-project for remote cluster proxy management",
		Long: help.Doc(`
				TBC
		`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	clientOptions := clientoptions.NewOptions()
	clientOptions.Complete()
	clientOptions.Validate()

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the controller manager",
		Long: help.Doc(`
			Start the controller manager

			The controller manager is in charge of starting the controllers reconciliating Proxy resources.
		`),
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// run as early as possible to avoid races later when some components (e.g. grpc) start early using klog
			if err := logsapiv1.ValidateAndApply(clientOptions.Logs, features.DefaultFeatureGate); err != nil {
				return err
			}

			logger := klog.FromContext(cmd.Context())
			logger.Info("instantiating manager")

			options := options.NewOptions()
			completedOptions, err := options.Complete()
			if err != nil {
				return err
			}
			config, err := manager.NewConfig(*completedOptions)
			if err != nil {
				return err
			}
			ctx := genericapiserver.SetupSignalContext()
			kcpClientConfigOverrides := &clientcmd.ConfigOverrides{
				CurrentContext: clientOptions.Context,
			}
			kcpClientConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				&clientcmd.ClientConfigLoadingRules{ExplicitPath: clientOptions.Kubeconfig},
				kcpClientConfigOverrides).ClientConfig()
			if err != nil {
				return err
			}
			kcpClientConfig.QPS = clientOptions.QPS
			kcpClientConfig.Burst = clientOptions.Burst

			// TODO (FGI) this needs to be amended so that the flag --cache-config is
			// taken in consideration
			cacheClientConfigOverrides := &clientcmd.ConfigOverrides{
				CurrentContext: "system:admin",
			}
			cacheClientConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				&clientcmd.ClientConfigLoadingRules{ExplicitPath: clientOptions.Kubeconfig},
				cacheClientConfigOverrides).ClientConfig()
			if err != nil {
				return err
			}
			cacheClientConfig.QPS = clientOptions.QPS
			cacheClientConfig.Burst = clientOptions.Burst
			cacheClientConfig = cacheclient.WithCacheServiceRoundTripper(cacheClientConfig)
			cacheClientConfig = cacheclient.WithShardNameFromContextRoundTripper(cacheClientConfig)
			cacheClientConfig = cacheclient.WithDefaultShardRoundTripper(cacheClientConfig, shard.Wildcard)

			mgr, err := manager.NewManager(ctx, config, kcpClientConfig, cacheClientConfig)
			if err != nil {
				return err
			}
			mgr.Start(ctx)
			if err != nil {
				return err
			}
			<-ctx.Done()
			logger.Info("stopping")
			return nil
		},
	}

	clientOptions.AddFlags(startCmd.Flags())

	if v := version.Get().String(); len(v) == 0 {
		startCmd.Version = "<unknown>"
	} else {
		startCmd.Version = v
	}

	cmd.AddCommand(startCmd)

	help.FitTerminal(cmd.OutOrStdout())

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	os.Exit(cli.Run(cmd))
}
