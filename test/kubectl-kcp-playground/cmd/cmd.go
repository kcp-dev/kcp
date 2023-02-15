/*
Copyright 2023 The KCP Authors.

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
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/component-base/version"

	"github.com/kcp-dev/kcp/test/kubectl-kcp-playground/plugin"
)

var (
	playgroundExample = `
	# Create a playground given a configuration file:
	%[1]s start -f test/kubectl-kcp-playground/examples/deploy-to-a-pool-of-clusters.yaml

	# Switch the kubectl context to work with a pcluster existing in the playground
	%[1]s use pcluster compute-1

	# Switch the kubectl context to work with a kcp shard existing in the playground
	%[1]s use shard main
`
)

func NewKubectlKcpPlaygroundCommand(streams genericclioptions.IOStreams) *cobra.Command {
	cliName := "kubectl kcp playground"

	rootCmd := &cobra.Command{
		Aliases:          []string{"kpl"},
		Use:              "kcp-playground [start|use]",
		Short:            "Manages local KCP playgrounds",
		Example:          fmt.Sprintf(playgroundExample, cliName),
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	if v := version.Get().String(); len(v) == 0 {
		rootCmd.Version = "<unknown>"
	} else {
		rootCmd.Version = v
	}

	startOpts := plugin.NewStartPlaygroundOptions(streams)
	startCmd := &cobra.Command{
		Use:          "start",
		Short:        "Starts a local KCP playground",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return cmd.Help()
			}
			if err := startOpts.Complete(args); err != nil {
				return err
			}
			if err := startOpts.Validate(); err != nil {
				return err
			}
			return startOpts.Run(cmd.Context())
		},
	}
	startOpts.BindFlags(startCmd)

	useCmd := &cobra.Command{
		Use:          "use [shard|pcluster]",
		Aliases:      []string{"use-context"},
		Short:        "Set the current-context for kubectl to connect to a shard or a pcluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	useShardOpts := plugin.NewUseOptions(streams)
	useShardCmd := &cobra.Command{
		Use:          "shard <name>",
		Short:        "Set the current-context for kubectl to connect to a shard",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			useShardOpts.Mode = plugin.UseShardMode
			if len(args) != 1 {
				return cmd.Help()
			}
			if err := useShardOpts.Complete(args); err != nil {
				return err
			}
			if err := useShardOpts.Validate(); err != nil {
				return err
			}
			return useShardOpts.Run(cmd.Context())
		},
	}
	useShardOpts.BindFlags(useShardCmd)

	usePClusterOpts := plugin.NewUseOptions(streams)
	usePClusterCmd := &cobra.Command{
		Use:          "pcluster <name>",
		Short:        "Set the current-context for kubectl to connect to a pcluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			usePClusterOpts.Mode = plugin.UsePClusterMode
			if len(args) != 1 {
				return cmd.Help()
			}
			if err := usePClusterOpts.Complete(args); err != nil {
				return err
			}
			if err := usePClusterOpts.Validate(); err != nil {
				return err
			}
			return usePClusterOpts.Run(cmd.Context())
		},
	}
	usePClusterOpts.BindFlags(usePClusterCmd)

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(useCmd)
	useCmd.AddCommand(useShardCmd)
	useCmd.AddCommand(usePClusterCmd)

	return rootCmd
}
