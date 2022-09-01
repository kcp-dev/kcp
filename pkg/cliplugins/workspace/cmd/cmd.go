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

	"github.com/kcp-dev/kcp/pkg/cliplugins/workspace/plugin"
)

var (
	workspaceExample = `
	# shows the workspace you are currently using
	%[1]s workspace .

	# enter a given workspace (this will change the current-context of your current KUBECONFIG)
	%[1]s workspace use my-workspace

	# short-hand for the use syntax
	%[1]s workspace my-workspace

	# list sub-workspaces in the current workspace 
	%[1]s get workspaces

	# enter a given absolute workspace
	%[1]s workspace root:default:my-workspace

	# enter the parent workspace
	%[1]s workspace ..

	# enter the previous workspace
	%[1]s workspace -

	# go to your home workspace 
	%[1]s workspace

	# create a workspace and immediately enter it
	%[1]s workspace create my-workspace --enter

	# create a context with the current workspace, e.g. root:default:my-workspace
	%[1]s workspace create-context

	# create a context with the current workspace, named context-name
	%[1]s workspace create-context context-name
`
)

// New returns a cobra.Command for workspace actions.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewOptions(streams)

	useWorkspaceOpts := plugin.NewUseWorkspaceOptions(streams)
	useRunE := func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return cmd.Help()
		}
		if err := useWorkspaceOpts.Complete(args); err != nil {
			return err
		}
		if err := useWorkspaceOpts.Validate(); err != nil {
			return err
		}
		return useWorkspaceOpts.Run(cmd.Context())
	}
	cmd := &cobra.Command{
		Aliases:          []string{"ws", "workspaces"},
		Use:              "workspace [create|create-context|use|current|<workspace>|..|.|-|~|<root:absolute:workspace>]",
		Short:            "Manages KCP workspaces",
		Example:          fmt.Sprintf(workspaceExample, "kubectl kcp"),
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE:             useRunE,
	}
	opts.BindFlags(cmd)

	useCmd := &cobra.Command{
		Use:          "use <workspace>|..|.|-|~|<root:absolute:workspace>",
		Short:        "Uses the given workspace as the current workspace. Using - means previous workspace, .. means parent workspace, . mean current, ~ means home workspace",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			return useRunE(c, args)
		},
	}

	currentWorkspaceOpts := plugin.NewCurrentWorkspaceOptions(streams)
	currentCmd := &cobra.Command{
		Use:          "current [--short]",
		Short:        "Print the current workspace. Same as 'kubectl ws .'.",
		Example:      "kcp workspace current",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				return cmd.Help()
			}
			if err := currentWorkspaceOpts.Validate(); err != nil {
				return err
			}
			if err := currentWorkspaceOpts.Complete(); err != nil {
				return err
			}
			return currentWorkspaceOpts.Run(c.Context())
		},
	}
	currentWorkspaceOpts.BindFlags(currentCmd)

	createWorkspaceOpts := plugin.NewCreateWorkspaceOptions(streams)
	createCmd := &cobra.Command{
		Use:          "create",
		Short:        "Creates a new workspace",
		Example:      "kcp workspace create <workspace name> [--type=<type>] [--enter [--ignore-not-ready]] --ignore-existing",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if err := createWorkspaceOpts.Validate(); err != nil {
				return err
			}
			if err := createWorkspaceOpts.Complete(args); err != nil {
				return err
			}
			return createWorkspaceOpts.Run(cmd.Context())
		},
	}
	createWorkspaceOpts.BindFlags(createCmd)

	createContextOpts := plugin.NewCreateContextOptions(streams)
	createContextCmd := &cobra.Command{
		Use:          "create-context [<context-name>] [--overwrite]",
		Short:        "Create a kubeconfig context for the current workspace",
		Example:      "kcp workspace create-context",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 1 {
				return cmd.Help()
			}
			if err := createContextOpts.Complete(args); err != nil {
				return err
			}
			if err := createContextOpts.Validate(); err != nil {
				return err
			}
			return createContextOpts.Run(c.Context())
		},
	}
	createContextOpts.BindFlags(createContextCmd)

	cmd.AddCommand(useCmd)
	cmd.AddCommand(currentCmd)
	cmd.AddCommand(createCmd)
	cmd.AddCommand(createContextCmd)
	return cmd, nil
}
