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

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/kcp/pkg/cliplugins/bind/plugin"
)

var (
	bindExampleUses = `
	# Create an APIBinding named "my-binding" that binds to the APIExport "my-export" in the "root:my-service" workspace.
	%[1]s bind apiexport root:my-service:my-export --name my-binding
	`

	bindComputeExampleUses = `
    # Create a placement to deploy standard kubernetes workloads to synctargets in the "root:mylocations" location workspace.
    %[1]s bind compute root:mylocations

    # Create a placement to deploy custom workloads to synctargets in the "root:mylocations" location workspace.
    %[1]s bind compute root:mylocations --apiexports=root:myapis:customapiexport

    # Create a placement to deploy standard kubernetes workloads to synctargets in the "root:mylocations" location workspace, and select only locations in the us-east region.
    %[1]s bind compute root:mylocations --location-selectors=region=us-east1
	`
)

func New(streams genericclioptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:              "bind",
		Short:            "Bind different types into current workspace.",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	bindOpts := plugin.NewBindOptions(streams)
	bindCmd := &cobra.Command{
		Use:          "apiexport <workspace_path:apiexport-name>",
		Short:        "Bind to an APIExport",
		Example:      fmt.Sprintf(bindExampleUses, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := bindOpts.Complete(args); err != nil {
				return err
			}

			if err := bindOpts.Validate(); err != nil {
				return err
			}

			return bindOpts.Run(cmd.Context())
		},
	}
	bindOpts.BindFlags(bindCmd)

	cmd.AddCommand(bindCmd)

	bindComputeOpts := plugin.NewBindComputeOptions(streams)
	bindComputeCmd := &cobra.Command{
		Use:          "compute <location workspace>",
		Short:        "Bind to a location workspace",
		Example:      fmt.Sprintf(bindComputeExampleUses, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := bindComputeOpts.Complete(args); err != nil {
				return err
			}

			if err := bindComputeOpts.Validate(); err != nil {
				return err
			}

			return bindComputeOpts.Run(cmd.Context())
		},
	}
	bindComputeOpts.BindFlags(bindComputeCmd)

	cmd.AddCommand(bindComputeCmd)
	return cmd
}
