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

	"github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
)

var (
	syncExample = `
	# Ensure a syncer is running on the specified sync target.
	%[1]s workload sync <sync-target-name> --syncer-image <kcp-syncer-image> -o syncer.yaml
	KUBECONFIG=<pcluster-config> kubectl apply -f syncer.yaml

	# Directly apply the manifest
	%[1]s workload sync <sync-target-name> --syncer-image <kcp-syncer-image> -o - | KUBECONFIG=<pcluster-config> kubectl apply -f -
`
	cordonExample = `
	# Mark a sync target as unschedulable.
	%[1]s workload cordon <sync-target-name>
`
	uncordonExample = `
	# Mark a sync target as schedulable.
	%[1]s workload uncordon <sync-target-name>
`
	drainExample = `
	# Start draining a sync target in preparation for maintenance.
	%[1]s workload drain <sync-target-name>
`
)

// New provides a cobra command for workload operations.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Aliases:          []string{"workloads"},
		Use:              "workload",
		Short:            "Manages KCP sync targets",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// Sync command
	syncOptions := plugin.NewSyncOptions(streams)

	enableSyncerCmd := &cobra.Command{
		Use:          "sync <sync-target-name> --syncer-image <kcp-syncer-image> [--resources=<resource1>,<resource2>..] -o <output-file>",
		Short:        "Create a synctarget in kcp with service account and RBAC permissions. Output a manifest to deploy a syncer for the given sync target in a physical cluster.",
		Example:      fmt.Sprintf(syncExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}

			if err := syncOptions.Complete(args); err != nil {
				return err
			}

			if err := syncOptions.Validate(); err != nil {
				return err
			}

			return syncOptions.Run(c.Context())
		},
	}

	syncOptions.BindFlags(enableSyncerCmd)
	cmd.AddCommand(enableSyncerCmd)

	// Cordon command
	cordonOpts := plugin.NewCordonOptions(streams)
	cordonOpts.Cordon = true

	cordonCmd := &cobra.Command{
		Use:          "cordon <sync-target-name>",
		Short:        "Mark sync target as unschedulable",
		Example:      fmt.Sprintf(cordonExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}

			if err := cordonOpts.Complete(args); err != nil {
				return err
			}

			if err := cordonOpts.Validate(); err != nil {
				return err
			}

			return cordonOpts.Run(c.Context())
		},
	}

	cordonOpts.BindFlags(cordonCmd)
	cmd.AddCommand(cordonCmd)

	// Uncordon command
	uncordonOpts := plugin.NewCordonOptions(streams)
	uncordonOpts.Cordon = false

	uncordonCmd := &cobra.Command{
		Use:          "uncordon <sync-target-name>",
		Short:        "Mark sync target as schedulable",
		Example:      fmt.Sprintf(uncordonExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}

			if err := uncordonOpts.Complete(args); err != nil {
				return err
			}

			if err := uncordonOpts.Validate(); err != nil {
				return err
			}

			return uncordonOpts.Run(c.Context())
		},
	}

	uncordonOpts.BindFlags(uncordonCmd)
	cmd.AddCommand(uncordonCmd)

	// Drain command
	drainOpts := plugin.NewDrainOptions(streams)

	drainCmd := &cobra.Command{
		Use:          "drain <sync-target-name>",
		Short:        "Start draining sync target in preparation for maintenance",
		Example:      fmt.Sprintf(drainExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}

			if err := drainOpts.Complete(args); err != nil {
				return err
			}

			if err := drainOpts.Validate(); err != nil {
				return err
			}

			return drainOpts.Run(c.Context())
		},
	}

	drainOpts.BindFlags(drainCmd)
	cmd.AddCommand(drainCmd)

	return cmd, nil
}
