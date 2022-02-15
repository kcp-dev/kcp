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
	# Shows the workspace you are currently using
	%[1]s workspace current

	# use a given workspace (this will change the current-context of your current KUBECONFIG)
	%[1]s workspace use

	# list all your personal workspaces
	%[1]s workspace list
`
)

// NewCmdWorkspace provides a cobra command wrapping WorkspaceOptions
func NewCmdWorkspace(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewOptions(streams)

	cmd := &cobra.Command{
		Use:              "workspace [--workspace-directory-server=] <current|use|list>",
		Short:            "Manages KCP workspaces",
		Example:          fmt.Sprintf(workspaceExample, "kubectl kcp"),
		SilenceUsage:     true,
		TraverseChildren: true,
	}

	opts.BindFlags(cmd)
	kubeconfig, err := plugin.NewKubeConfig(opts)
	if err != nil {
		return nil, err
	}

	useCmd := &cobra.Command{
		Use:          "use < workspace name | - >",
		Short:        "Uses the given workspace as the current workspace. Using - means previous workspace",
		Example:      "kcp workspace use my-worspace",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("The workspace name (or -) should be given")
			}

			if err := kubeconfig.UseWorkspace(c.Context(), opts, args[0]); err != nil {
				return err
			}
			return nil
		},
	}

	currentCmd := &cobra.Command{
		Use:          "current",
		Short:        "Returns the name of the current workspace",
		Example:      "kcp workspace current",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := kubeconfig.CurrentWorkspace(c.Context(), opts); err != nil {
				return err
			}
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:          "list",
		Short:        "Returns the list of the personal workspaces of the user",
		Example:      "kcp workspace list",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := kubeconfig.ListWorkspaces(c.Context(), opts); err != nil {
				return err
			}
			return nil
		},
	}

	useFlag := "use"
	createCmd := &cobra.Command{
		Use:          "create",
		Short:        "Creates a new personal workspace",
		Example:      "kcp workspace create <workspace name>",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			useAfterCreation, err := c.Flags().GetBool(useFlag)
			if err != nil {
				return err
			}
			if err := kubeconfig.CreateWorkspace(c.Context(), opts, args[0], useAfterCreation); err != nil {
				return err
			}
			return nil
		},
	}
	createCmd.Flags().Bool(useFlag, false, "Use the new workspace after a successful creation")

	deleteCmd := &cobra.Command{
		Use:          "delete",
		Short:        "Deletes a personal workspace",
		Example:      "kcp workspace delete <workspace name>",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := kubeconfig.DeleteWorkspace(c.Context(), opts, args[0]); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.AddCommand(useCmd)
	cmd.AddCommand(currentCmd)
	cmd.AddCommand(listCmd)
	cmd.AddCommand(createCmd)
	cmd.AddCommand(deleteCmd)
	return cmd, nil
}
