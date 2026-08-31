/*
Copyright 2026 The kcp Authors.

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

	"github.com/kcp-dev/cli/pkg/shard/plugin"
)

var (
	cordonExample = `
# Mark shard "alpha" as unschedulable: no new workspaces are scheduled onto it
%[1]s shard cordon alpha
`
	uncordonExample = `
# Mark shard "alpha" as schedulable again
%[1]s shard uncordon alpha
`
)

// New provides a command for shard operations.
func New(streams genericclioptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:              "shard",
		Short:            "Manages kcp shards",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cordonOptions := plugin.NewCordonOptions(streams)
	cordonOptions.Cordon = true
	cordonCommand := &cobra.Command{
		Use:          "cordon SHARD",
		Short:        "Mark shard as unschedulable",
		Long:         "Mark shard as unschedulable: the workspace scheduler will not place new workspaces on it. The shard acknowledges via the Schedulable condition on its Shard object in the root workspace.",
		Example:      fmt.Sprintf(cordonExample, "kubectl kcp"),
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := cordonOptions.Complete(args); err != nil {
				return err
			}
			if err := cordonOptions.Validate(); err != nil {
				return err
			}
			return cordonOptions.Run(c.Context())
		},
	}
	cordonOptions.BindFlags(cordonCommand)
	cmd.AddCommand(cordonCommand)

	uncordonOptions := plugin.NewCordonOptions(streams)
	uncordonOptions.Cordon = false
	uncordonCommand := &cobra.Command{
		Use:          "uncordon SHARD",
		Short:        "Mark shard as schedulable",
		Long:         "Mark shard as schedulable again, allowing the workspace scheduler to place new workspaces on it.",
		Example:      fmt.Sprintf(uncordonExample, "kubectl kcp"),
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := uncordonOptions.Complete(args); err != nil {
				return err
			}
			if err := uncordonOptions.Validate(); err != nil {
				return err
			}
			return uncordonOptions.Run(c.Context())
		},
	}
	uncordonOptions.BindFlags(uncordonCommand)
	cmd.AddCommand(uncordonCommand)

	return cmd
}
