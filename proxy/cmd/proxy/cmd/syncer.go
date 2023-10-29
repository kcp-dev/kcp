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

package cmd

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/spf13/cobra"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	proxyoptions "github.com/kcp-dev/kcp/proxy/cmd/proxy/options"
	"github.com/kcp-dev/kcp/proxy/proxy"
)

const numThreads = 2

func NewProxyCommand() *cobra.Command {
	rand.Seed(time.Now().UTC().UnixNano())

	options := proxyoptions.NewOptions()
	proxyCommand := &cobra.Command{
		Use:   "proxy",
		Short: "Proxies resources from one cluster to another",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := logsapiv1.ValidateAndApply(options.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := options.Complete(); err != nil {
				return err
			}

			if err := options.Validate(); err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()
			if err := Run(ctx, options); err != nil {
				return err
			}

			<-ctx.Done()

			return nil
		},
	}

	options.AddFlags(proxyCommand.Flags())

	if v := version.Get().String(); len(v) == 0 {
		proxyCommand.Version = "<unknown>"
	} else {
		proxyCommand.Version = v
	}

	return proxyCommand
}

func Run(ctx context.Context, options *proxyoptions.Options) error {
	logger := klog.FromContext(ctx)
	logger.Info("proxying")

	kcpConfigOverrides := &clientcmd.ConfigOverrides{
		CurrentContext: options.FromContext,
	}
	upstreamConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.FromKubeconfig},
		kcpConfigOverrides).ClientConfig()
	if err != nil {
		return err
	}

	upstreamConfig.QPS = options.QPS
	upstreamConfig.Burst = options.Burst

	downstreamConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.ToKubeconfig},
		&clientcmd.ConfigOverrides{
			CurrentContext: options.ToContext,
		}).ClientConfig()
	if err != nil {
		return err
	}

	downstreamConfig.QPS = options.QPS
	downstreamConfig.Burst = options.Burst

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		return errors.New("missing environment variable: NAMESPACE")
	}

	return proxy.StartProxy(
		ctx,
		&proxy.ProxyConfig{
			UpstreamConfig:   upstreamConfig,
			DownstreamConfig: downstreamConfig,
			ProxyTargetPath:  logicalcluster.NewPath(options.FromClusterPath),
			ProxyTargetName:  options.ProxyTargetName,
			ProxyTargetUID:   options.ProxyTargetUID,
		},
		numThreads,
		namespace,
	)
}
