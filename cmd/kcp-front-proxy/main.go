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
	"context"
	goflags "flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	restclient "k8s.io/client-go/rest"
	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/version"

	frontproxyoptions "github.com/kcp-dev/kcp/cmd/kcp-front-proxy/options"
	"github.com/kcp-dev/kcp/pkg/proxy"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()

	rand.Seed(time.Now().UTC().UnixNano())

	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflags.CommandLine)

	logs.InitLogs()
	defer logs.FlushLogs()

	command := NewProxyCommand(ctx)
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func NewProxyCommand(ctx context.Context) *cobra.Command {
	options := frontproxyoptions.NewOptions()
	cmd := &cobra.Command{
		Use:   "kcp-front-proxy",
		Short: "Terminate TLS and handles client cert auth for backend API servers",
		Long: `kcp-front-proxy is a reverse proxy that accepts client certificates and
forwards Common Name and Organizations to backend API servers in HTTP headers.
The proxy terminates TLS and communicates with API servers via mTLS. Traffic is
routed based on paths.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Complete(); err != nil {
				return err
			}
			if errs := options.Validate(); errs != nil {
				return errors.NewAggregate(errs)
			}
			proxyHandler, err := proxy.NewHandler(&options.Proxy)
			if err != nil {
				return err
			}

			var servingInfo *genericapiserver.SecureServingInfo
			var loopbackClientConfig *restclient.Config
			if err := options.SecureServing.ApplyTo(&servingInfo, &loopbackClientConfig); err != nil {
				return err
			}
			doneCh, err := servingInfo.Serve(proxyHandler, time.Second*60, ctx.Done())
			if err != nil {
				return err
			}

			<-doneCh
			return nil
		},
	}

	options.AddFlags(cmd.Flags())

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	return cmd
}
