/*
Copyright 2022 The kcp Authors.

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
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/term"
	"k8s.io/component-base/version"

	frontproxyoptions "github.com/kcp-dev/kcp/cmd/kcp-front-proxy/options"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/proxy"

	_ "k8s.io/component-base/logs/json/register"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()

	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflags.CommandLine)

	cmd := NewProxyCommand(ctx)
	code := cli.Run(cmd)
	os.Exit(code)
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
			if err := logsapiv1.ValidateAndApply(options.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := options.Complete(); err != nil {
				return err
			}
			if errs := options.Validate(); errs != nil {
				return utilerrors.NewAggregate(errs)
			}

			if options.Proxy.ProfilerAddress != "" {
				//nolint:errcheck,gosec
				go http.ListenAndServe(options.Proxy.ProfilerAddress, nil)
			}

			config, err := proxy.NewConfig(ctx, options.Proxy)
			if err != nil {
				return err
			}
			completedConfig, err := config.Complete()
			if err != nil {
				return err
			}

			server, err := proxy.NewServer(ctx, completedConfig)
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

	// add start named flag sets to start flags
	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)
	globalflag.AddGlobalFlags(fss.FlagSet("global"), cmd.Name(), logs.SkipLoggingConfigurationFlags())
	startFlags := cmd.Flags()
	for _, f := range fss.FlagSets {
		startFlags.AddFlagSet(f)
	}

	// set usage and help function to print sectioned flags
	usageFmt := "Usage:\n  %s\n"
	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())
	cmd.SetUsageFunc(func(cmd *cobra.Command) error {
		fmt.Fprintf(cmd.OutOrStderr(), usageFmt, cmd.UseLine())
		cliflag.PrintSections(cmd.OutOrStderr(), fss, cols)
		return nil
	})
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n"+usageFmt, cmd.Long, cmd.UseLine())
		cliflag.PrintSections(cmd.OutOrStdout(), fss, cols)
	})

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	return cmd
}
