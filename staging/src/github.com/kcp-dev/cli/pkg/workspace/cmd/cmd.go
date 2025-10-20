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
	"k8s.io/component-base/version"

	"github.com/kcp-dev/cli/pkg/workspace/plugin"
)

var (
	workspaceExample = `
# shows the workspace you are currently using
%[1]s workspace .

# interactive workspace tree browser
%[1]s workspace -i

# enter a given workspace (this will change the current-context of your current KUBECONFIG)
%[1]s workspace use my-workspace

# short-hand for the use syntax
%[1]s workspace my-workspace

# enter a given absolute workspace
%[1]s workspace :root:default:my-workspace

# short-hand for the current root workspace
%[1]s workspace :

# enter a given relative workspace
%[1]s workspace some:nested:workspace

# enter the parent workspace
%[1]s workspace ..

# enter the grand parent workspace
%[1]s workspace ..:..

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
	cmdOpts := plugin.NewUseWorkspaceOptions(streams)
	treeOpts := plugin.NewTreeOptions(streams)

	cliName := "kubectl"
	if pflag.CommandLine.Name() == "kubectl-kcp" {
		cliName = "kubectl kcp"
	}

	var interactive bool

	cmd := &cobra.Command{
		Aliases:          []string{"ws", "workspaces"},
		Use:              "workspace [create|create-context|use|current|<workspace>|..|.|-|~|<root:absolute:workspace>] [-i|--interactive]",
		Short:            "Manages kcp workspaces",
		Example:          fmt.Sprintf(workspaceExample, cliName),
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if interactive {
				if len(args) != 0 {
					return errors.New("interactive mode does not accept arguments")
				}
				treeOpts.Interactive = true
				if err := treeOpts.Validate(); err != nil {
					return err
				}
				if err := treeOpts.Complete(); err != nil {
					return err
				}
				return treeOpts.Run(cmd.Context())
			}

			if len(args) > 1 {
				return cmd.Help()
			}
			if err := cmdOpts.Complete(args); err != nil {
				return err
			}
			if err := cmdOpts.Validate(); err != nil {
				return err
			}
			return cmdOpts.Run(cmd.Context())
		},
	}

	// Add interactive flag
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Interactive workspace tree browser")

	cmdOpts.BindFlags(cmd)

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	useWorkspaceOpts := plugin.NewUseWorkspaceOptions(streams)
	useCmd := &cobra.Command{
		Aliases:      []string{"cd"},
		Use:          "use <workspace>|..|.|-|~|<:root:absolute:workspace>|<relative:workspace>",
		Short:        "Uses the given workspace as the current workspace. Using - means previous workspace, .. means parent workspace, . mean current, ~ means home workspace",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}
			if err := useWorkspaceOpts.Complete(args); err != nil {
				return err
			}
			if err := useWorkspaceOpts.Validate(); err != nil {
				return err
			}
			return useWorkspaceOpts.Run(c.Context())
		},
	}
	useWorkspaceOpts.BindFlags(useCmd)

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

	createCmd, err := NewCreate(cliName+" workspace create", "install the 'kubectl create workspace' plugin instead, compare https://docs.kcp.io/kcp/latest/setup/kubectl-plugin/.", streams)
	if err != nil {
		return nil, err
	}

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

	treeCmdOpts := plugin.NewTreeOptions(streams)
	treeCmd := &cobra.Command{
		Use:          "tree",
		Short:        "Print the current workspace tree.",
		Example:      "kcp workspace tree",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				return cmd.Help()
			}
			if err := treeCmdOpts.Validate(); err != nil {
				return err
			}
			if err := treeCmdOpts.Complete(); err != nil {
				return err
			}
			return treeCmdOpts.Run(c.Context())
		},
	}
	treeCmdOpts.BindFlags(treeCmd)

	cmd.AddCommand(useCmd)
	cmd.AddCommand(treeCmd)
	cmd.AddCommand(currentCmd)
	cmd.AddCommand(createCmd)
	cmd.AddCommand(createContextCmd)
	return cmd, nil
}

// NewCreate returns a cobra.Command for workspace create action.
func NewCreate(prefix string, deprecation string, streams genericclioptions.IOStreams) (*cobra.Command, error) {
	createWorkspaceOpts := plugin.NewCreateWorkspaceOptions(streams)
	cmd := &cobra.Command{
		Use:          "create",
		Short:        "Creates a new workspace",
		Example:      prefix + " <workspace name> [--type=<type>] [--enter [--ignore-not-ready]] --ignore-existing",
		Deprecated:   deprecation,
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
	createWorkspaceOpts.BindFlags(cmd)

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	return cmd, nil
}
