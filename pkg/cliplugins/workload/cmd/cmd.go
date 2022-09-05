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
	"strings"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
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
	opts := plugin.NewOptions(streams)

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
	opts.BindFlags(cmd)

	// Any user-specified types will be synced in addition to the required types
	// to ensure support for the use case of a synced deployment capable of
	// talking to kcp.
	//
	// TODO(marun) Consider allowing a user-specified and exclusive set of types.
	requiredResourcesToSync := sets.NewString("deployments.apps", "secrets", "configmaps", "serviceaccounts")

	var (
		userResourcesToSync []string
		syncerImage         string
		replicas            = 1
		outputFile          string
		downstreamNamespace string
		featureGatesString  string
		kcpNamespace                = "default"
		qps                 float32 = 30
		burst                       = 20
	)

	enableSyncerCmd := &cobra.Command{
		Use:          "sync <sync-target-name> --syncer-image <kcp-syncer-image> [--resources=<resource1>,<resource2>..] -o <output-file>",
		Short:        "Create a synctarget in kcp with service account and RBAC permissions. Output a manifest to deploy a syncer for the given sync target in a physical cluster.",
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

			if len(kcpNamespace) == 0 {
				return errors.New("a value must be specified for --kcp-namespace")
			}

			if replicas < 0 {
				return errors.New("a non-negative value must be specified for --replicas")
			}
			if replicas > 1 {
				// TODO: relax when we have leader-election in the syncer
				return errors.New("only 0 and 1 are allowed as --replicas values")
			}
			if len(outputFile) == 0 {
				return errors.New("a value must be specified for --output-file")
			}

			syncTargetName := args[0]
			if len(syncTargetName)+len(plugin.SyncerIDPrefix)+8 > 254 {
				return fmt.Errorf("the maximum length of the sync-target-name is %d", plugin.MaxSyncTargetNameLength)
			}

			resourcesToSync := sets.NewString(userResourcesToSync...).Union(requiredResourcesToSync).List()

			return kubeconfig.Sync(
				c.Context(),
				outputFile,
				syncTargetName,
				kcpNamespace,
				downstreamNamespace,
				syncerImage,
				resourcesToSync,
				replicas,
				qps,
				burst,
				featureGatesString,
			)
		},
	}
	enableSyncerCmd.Flags().StringSliceVar(&userResourcesToSync, "resources", userResourcesToSync, "Resources to synchronize with kcp.")
	enableSyncerCmd.Flags().StringVar(&syncerImage, "syncer-image", syncerImage, "The syncer image to use in the syncer's deployment YAML. Images are published at https://github.com/kcp-dev/kcp/pkgs/container/kcp%2Fsyncer.")
	enableSyncerCmd.Flags().IntVar(&replicas, "replicas", replicas, "Number of replicas of the syncer deployment.")
	enableSyncerCmd.Flags().StringVar(&kcpNamespace, "kcp-namespace", kcpNamespace, "The name of the kcp namespace to create a service account in.")
	enableSyncerCmd.Flags().StringVarP(&outputFile, "output-file", "o", outputFile, "The manifest file to be created and applied to the physical cluster. Use - for stdout.")
	enableSyncerCmd.Flags().StringVarP(&downstreamNamespace, "namespace", "n", downstreamNamespace, "The namespace to create the syncer in in the physical cluster. By default this is \"kcp-syncer-<synctarget-name>-<uid>\".")
	enableSyncerCmd.Flags().Float32Var(&qps, "qps", qps, "QPS to use when talking to API servers.")
	enableSyncerCmd.Flags().IntVar(&burst, "burst", burst, "Burst to use when talking to API servers.")
	enableSyncerCmd.Flags().StringVar(&featureGatesString, "feature-gates", "",
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(kcpfeatures.KnownFeatures(), "\n")) // hide kube-only gates

	cmd.AddCommand(enableSyncerCmd)

	// cordon
	cordonCmd := &cobra.Command{
		Use:          "cordon <sync-target-name>",
		Short:        "Mark sync target as unschedulable",
		Example:      fmt.Sprintf(cordonExample, "kubectl kcp"),
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

			syncTargetName := args[0]

			return kubeconfig.Cordon(c.Context(), syncTargetName)
		},
	}

	cmd.AddCommand(cordonCmd)

	// uncordon
	uncordonCmd := &cobra.Command{
		Use:          "uncordon <sync-target-name>",
		Short:        "Mark sync target as schedulable",
		Example:      fmt.Sprintf(uncordonExample, "kubectl kcp"),
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

			syncTargetName := args[0]

			return kubeconfig.Uncordon(c.Context(), syncTargetName)
		},
	}

	cmd.AddCommand(uncordonCmd)

	// drain
	drainCmd := &cobra.Command{
		Use:          "drain <sync-target-name>",
		Short:        "Start draining sync target in preparation for maintenance",
		Example:      fmt.Sprintf(drainExample, "kubectl kcp"),
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

			syncTargetName := args[0]

			return kubeconfig.Drain(c.Context(), syncTargetName)
		},
	}

	cmd.AddCommand(drainCmd)

	return cmd, nil
}
