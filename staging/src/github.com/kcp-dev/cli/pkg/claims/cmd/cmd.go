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
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/cli/pkg/claims/plugin"
)

// TODO: Add examples for edit and update claims.
var (
	claimsExample = `
# Lists the permission claims and their respective status related to a specific APIBinding.
%[1]s claims get apibinding cert-manager

# List permission claims and their respective status for all APIBindings in current workspace.
%[1]s claims get apibinding
`
)

// New returns a cobra.Command for claims related actions.
func New(streams genericclioptions.IOStreams) *cobra.Command {
	cliName := "kubectl"
	if pflag.CommandLine.Name() == "kubectl-kcp" {
		cliName = "kubectl kcp"
	}

	claimsCmd := &cobra.Command{
		Use:              "claims",
		Short:            "Operations related to viewing or updating permission claims",
		SilenceUsage:     true,
		Example:          fmt.Sprintf(claimsExample, cliName),
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	getcmd := &cobra.Command{
		Use:              "get",
		Short:            "Operations related to fetching APIs with respect to permission claims",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	apibindingGetOpts := plugin.NewGetAPIBindingOptions(streams)
	apibindingGetCmd := &cobra.Command{
		Use:          "apibinding <apibinding_name>",
		Short:        "Get claims related to apibinding",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := apibindingGetOpts.Complete(args); err != nil {
				return err
			}
			if err := apibindingGetOpts.Validate(); err != nil {
				return err
			}
			return apibindingGetOpts.Run(cmd.Context())
		},
	}
	apibindingGetOpts.BindFlags(apibindingGetCmd)
	getcmd.AddCommand(apibindingGetCmd)
	claimsCmd.AddCommand(getcmd)
	return claimsCmd
}
