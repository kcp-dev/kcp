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
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/kcp/pkg/cliplugins/workspace/plugin"
)

var (
	workspaceExample = `
	# Shows the workspace you are currently using
	%[1]s workspace

	# enter a given workspace (this will change the current-context of your current KUBECONFIG)
	%[1]s workspace use my-workspace

	# list all your personal workspaces
	%[1]s workspace list

	# enter a given absolute workspace
	%[1]s workspace root:default:my-workspace

	# enter the parent workspace
	%[1]s workspace ..

	# enter the previous workspace
	%[1]s workspace -

	# create a workspace and immediately enter it
	%[1]s workspace create my-workspace --use

	# create a context with the current workspace, e.g. root:default:my-workspace
	%[1]s workspace create-context

	# create a context with the current workspace, named context-name
	%[1]s workspace create-context context-name

	# enable a syncer for my-pcluster
	%[1]s workspace enable-syncer my-pcluster
`
)

// NewCmdWorkspace provides a cobra command wrapping WorkspaceOptions
func NewCmdWorkspace(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewOptions(streams)

	useRunE := func(cmd *cobra.Command, args []string) error {
		if err := opts.Validate(); err != nil {
			return err
		}

		kubeconfig, err := plugin.NewKubeConfig(opts)
		if err != nil {
			return err
		}
		if len(args) > 1 {
			return cmd.Help()
		}

		arg := ""
		if len(args) == 1 {
			arg = args[0]
		}
		return kubeconfig.UseWorkspace(cmd.Context(), arg)
	}
	cmd := &cobra.Command{
		Aliases:          []string{"ws", "workspaces"},
		Use:              "workspace [list|create|create-context|<workspace>|..|-|<root:absolute:workspace>]",
		Short:            "Manages KCP workspaces",
		Example:          fmt.Sprintf(workspaceExample, "kubectl kcp"),
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE:             useRunE,
	}
	opts.BindFlags(cmd)

	useCmd := &cobra.Command{
		Use:          "use <workspace>|..|-|<root:absolute:workspace>",
		Short:        "Uses the given workspace as the current workspace. Using - means previous workspace, .. means parent workspace",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			return useRunE(c, args)
		},
	}

	var shortWorkspaceOutput bool
	currentCmd := &cobra.Command{
		Use:          "current [--short]",
		Short:        "Print the current workspace",
		Example:      "kcp workspace current",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}

			kubeconfig, err := plugin.NewKubeConfig(opts)
			if err != nil {
				return err
			}
			if len(args) != 0 {
				return cmd.Help()
			}
			return kubeconfig.CurrentWorkspace(c.Context(), shortWorkspaceOutput)
		},
	}
	currentCmd.Flags().BoolVar(&shortWorkspaceOutput, "short", shortWorkspaceOutput, "Print only the name of the workspace, e.g. for integration into the shell prompt")

	listCmd := &cobra.Command{
		Use:          "list",
		Short:        "Returns the list of the personal workspaces of the user",
		Example:      "kcp workspace list",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			kubeconfig, err := plugin.NewKubeConfig(opts)
			if err != nil {
				return err
			}
			if err := kubeconfig.ListWorkspaces(c.Context(), opts); err != nil {
				return err
			}
			return nil
		},
	}

	var workspaceType string
	var enterAfterCreation bool
	var ignoreExisting bool
	createCmd := &cobra.Command{
		Use:          "create",
		Short:        "Creates a new personal workspace",
		Example:      "kcp workspace create <workspace name> [--type=<type>] [--enter [--ignore-not-ready]] --ignore-existing",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			kubeconfig, err := plugin.NewKubeConfig(opts)
			if err != nil {
				return err
			}
			return kubeconfig.CreateWorkspace(cmd.Context(), args[0], workspaceType, ignoreExisting, enterAfterCreation, time.Minute)
		},
	}
	createCmd.Flags().StringVar(&workspaceType, "type", "", "A workspace type (default: Universal)")
	createCmd.Flags().BoolVar(&enterAfterCreation, "enter", enterAfterCreation, "Immediately enter the created workspace")
	createCmd.Flags().BoolVar(&ignoreExisting, "ignore-existing", ignoreExisting, "Ignore if the workspace already exists")
	createCmd.Flags().BoolVar(&enterAfterCreation, "use", enterAfterCreation, "Use the new workspace after a successful creation")
	if err := createCmd.Flags().MarkDeprecated("use", "Use --enter instead"); err != nil {
		return nil, err
	}

	var overwriteContext bool
	createContextCmd := &cobra.Command{
		Use:          "create-context [<context-name>] [--overwrite]",
		Short:        "Create a kubeconfig context for the current workspace",
		Example:      "kcp workspace create-context",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			kubeconfig, err := plugin.NewKubeConfig(opts)
			if err != nil {
				return err
			}

			if len(args) > 1 {
				return cmd.Help()
			}

			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}

			return kubeconfig.CreateContext(cmd.Context(), arg, overwriteContext)
		},
	}
	createContextCmd.Flags().BoolVar(&overwriteContext, "overwrite", overwriteContext, "Overwrite the context if it already exists")

	// TODO(marun) Maybe require secrets and serviceaccounts if deployments.apps is manually specified to ensure access to kcp api?
	resourcesToSync := []string{"deployment.apps.k8s.io/v1", "secrets/v1", "configmaps/v1", "serviceaccounts/v1"}
	var syncerImage string
	enableSyncerCmd := &cobra.Command{
		Use:          "enable-syncer <workload-cluster-name> [--sync-resources=<resource1>,<resource2>..]",
		Short:        "Enable a syncer to be deployed for the named workload cluster",
		Example:      "kcp workspace enable-syncer my-workload-cluster",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			kubeconfig, err := plugin.NewKubeConfig(opts)
			if err != nil {
				return err
			}

			if len(args) != 1 {
				return cmd.Help()
			}

			if len(resourcesToSync) == 0 {
				return errors.New("One or more values must be specified for --sync-resources")
			}

			if len(syncerImage) == 0 {
				return errors.New("A value must be specified for --syncer-image")
			}

			workloadClusterName := args[0]

			return kubeconfig.EnableSyncer(c.Context(), workloadClusterName, syncerImage, resourcesToSync)
		},
	}
	enableSyncerCmd.Flags().StringSliceVar(&resourcesToSync, "sync-resources", resourcesToSync, "Resources to synchronize with kcp.")
	enableSyncerCmd.Flags().StringVar(&syncerImage, "syncer-image", syncerImage, "The syncer image to use in the syncer's deployment YAML.")

	deleteCmd := &cobra.Command{
		Use:          "delete",
		Short:        "Replaced with \"kubectl delete workspace <workspace-name>\"",
		Example:      "kcp workspace delete <workspace-name>",
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		Run: func(c *cobra.Command, args []string) {
			fmt.Println("The \"delete\" command is gone. Please do instead:\n\n  kubectl delete workspace <workspace-name>")
		},
	}

	cmd.AddCommand(useCmd)
	cmd.AddCommand(currentCmd)
	cmd.AddCommand(listCmd)
	cmd.AddCommand(createCmd)
	cmd.AddCommand(createContextCmd)
	cmd.AddCommand(enableSyncerCmd)
	cmd.AddCommand(deleteCmd)
	return cmd, nil
}
