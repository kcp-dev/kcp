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

	"github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
)

var (
	syncExample = `
	# Ensure a syncer is running on the specified workload cluster.
	%[1]s workload sync <workload-cluster-name> --syncer-image <kcp-syncer-image>
`
)

// New provides a cobra command for workload operations.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	opts := plugin.NewOptions(streams)

	cmd := &cobra.Command{
		Aliases:          []string{"workload", "workloads"},
		Use:              "workspace",
		Short:            "Manages KCP workload clusters",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	opts.BindFlags(cmd)

	// Any user-specified types will be synced in addition to the required types
	// to ensure support for the use case of a synced deployment capable of
	// talking to kcp.
	//
	// TODO(marun) Consider allowing a user-specified and exclusive set of types.
	requiredResourcesToSync := sets.NewString("deployments.apps", "secrets", "configmaps", "serviceaccounts")

	var userResourcesToSync []string
	var syncerImage string
	var replicas int = 1
	kcpNamespaceName := "default"
	enableSyncerCmd := &cobra.Command{
		Use:          "sync <workload-cluster-name> --syncer-image <kcp-syncer-image> [--resources=<resource1>,<resource2>..]",
		Short:        "Deploy a syncer for the given workload cluster",
		Example:      fmt.Sprintf(syncExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			kubeconfig, err := plugin.NewConfig(opts)
			if err != nil {
				return err
			}

			if len(args) != 1 {
				return cmd.Help()
			}

			if len(syncerImage) == 0 {
				return errors.New("a value must be specified for --syncer-image")
			}

			if len(kcpNamespaceName) == 0 {
				return errors.New("a value must be specified for --kcp-namespace")
			}

			if replicas < 0 {
				return errors.New("a non-negative value must be specified for --replicas")
			}
			if replicas > 1 {
				// TODO: relax when we have leader-election in the syncer
				return errors.New("only 0 and 1 are allowed as --replicas values")
			}

			workloadClusterName := args[0]
			if len(workloadClusterName)+len(plugin.SyncerAuthResourcePrefix) > plugin.MaxSyncerAuthResourceName {
				return fmt.Errorf("the maximum length of the workload-cluster-name is %d", plugin.MaxSyncerAuthResourceName)
			}

			resourcesToSync := sets.NewString(userResourcesToSync...).Union(requiredResourcesToSync).List()

			return kubeconfig.Sync(c.Context(), workloadClusterName, kcpNamespaceName, syncerImage, resourcesToSync, replicas)
		},
	}
	enableSyncerCmd.Flags().StringSliceVar(&userResourcesToSync, "resources", userResourcesToSync, "Resources to synchronize with kcp.")
	enableSyncerCmd.Flags().StringVar(&syncerImage, "syncer-image", syncerImage, "The syncer image to use in the syncer's deployment YAML.")
	enableSyncerCmd.Flags().IntVar(&replicas, "replicas", replicas, "Number of replicas of the syncer deployment.")
	enableSyncerCmd.Flags().StringVar(&kcpNamespaceName, "kcp-namespace", kcpNamespaceName, "The name of the kcp namespace to create a service account in.")

	cmd.AddCommand(enableSyncerCmd)

	return cmd, nil
}
