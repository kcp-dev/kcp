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

	"github.com/kcp-dev/cli/pkg/bind/plugin"
)

var (
	bindExampleUses = `
# Create an APIBinding named "my-binding" that binds to the APIExport "my-export" in the "root:my-service" workspace.
%[1]s bind apiexport root:my-service:my-export --name my-binding

# Create an APIBinding named "my-binding" that binds to the APIExport "my-export" in the "root:my-service" workspace with accepted permission claims.
%[1]s bind apiexport root:my-service:my-export --name my-binding --accept-permission-claim secrets.core,configmaps.core

# Create an APIBinding named "my-binding" that binds to the APIExport "my-export" in the "root:my-service" workspace with rejected permission claims.
%[1]s bind apiexport root:my-service:my-export --name my-binding --reject-permission-claim secrets.core,configmaps.core
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
	return cmd
}
