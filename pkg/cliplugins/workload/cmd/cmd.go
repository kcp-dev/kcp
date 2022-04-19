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

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	plugin2 "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	"github.com/kcp-dev/kcp/pkg/cliplugins/workspace/plugin"
)

var (
	workspaceExample = `
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

	// Any user-specified types will be synced in addition to the required types
	// to ensure support for the use case of a synced deployment capable of
	// talking to kcp.
	//
	// TODO(marun) Consider allowing a user-specified and exclusive set of types.
	requiredResourcesToSync := sets.NewString("deployments.apps", "secrets", "configmaps", "serviceaccounts")

	var resourcesToSync []string
	var syncerImage string
	disableDeployment := false
	kcpNamespaceName := "default"
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

			if len(syncerImage) == 0 {
				return errors.New("A value must be specified for --syncer-image")
			}

			if len(kcpNamespaceName) == 0 {
				return errors.New("A value must be specified for --kcp-namespace")
			}

			workloadClusterName := args[0]
			if len(workloadClusterName)+len(plugin2.SyncerAuthResourcePrefix) > plugin2.MaxSyncerAuthResourceName {
				return fmt.Errorf("The maximum length of the workload-cluster-name is %d", plugin2.MaxSyncerAuthResourceName)
			}

			requiredResourcesToSync.Insert(resourcesToSync...)

			return kubeconfig.EnableSyncer(c.Context(), workloadClusterName, kcpNamespaceName, syncerImage, resourcesToSync, disableDeployment)
		},
	}
	enableSyncerCmd.Flags().StringSliceVar(&resourcesToSync, "sync-resources", resourcesToSync, "Resources to synchronize with kcp.")
	enableSyncerCmd.Flags().StringVar(&syncerImage, "syncer-image", syncerImage, "The syncer image to use in the syncer's deployment YAML.")
	enableSyncerCmd.Flags().BoolVar(&disableDeployment, "disable-deployment", disableDeployment, "Whether to configure the syncer deployment with zero replicas.")
	enableSyncerCmd.Flags().StringVar(&kcpNamespaceName, "kcp-namespace", kcpNamespaceName, "The name of the kcp namespace to create a service account in.")

	cmd.AddCommand(enableSyncerCmd)
	return cmd, nil
}
