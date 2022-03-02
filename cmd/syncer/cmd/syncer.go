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
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"

	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

const numThreads = 2

func NewSyncerCommand() *cobra.Command {
	options := NewDefaultOptions()

	syncerCommand := &cobra.Command{
		Use:   "syncer",
		Short: "Synchronizes resources in `kcp` assigned to the clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Complete(); err != nil {
				return err
			}

			if err := options.Validate(); err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()
			if err := options.Run(ctx); err != nil {
				return err
			}

			<-ctx.Done()

			return nil
		},
	}

	options.BindFlags(syncerCommand.Flags())

	return syncerCommand
}

func (options *Options) Validate() error {
	if options.FromClusterName == "" {
		return errors.New("--from-cluster is required")
	}

	return nil
}

func (options *Options) Complete() error {
	klog.Infof("Syncing the following resource types: %s", options.SyncedResourceTypes)

	// Create a client to dynamically watch "from".
	if cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.FromKubeconfig}, nil).ClientConfig(); err != nil {
		return err
	} else {
		options.fromConfig = cfg
	}

	if options.ToKubeconfig != "" {
		toOverrides := clientcmd.ConfigOverrides{
			CurrentContext: options.ToContext,
		}

		if cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.ToKubeconfig},
			&toOverrides).ClientConfig(); err != nil {
			return err
		} else {
			options.toConfig = cfg
		}
	}

	return nil
}

func (options *Options) Run(ctx context.Context) error {
	if err := syncer.StartSyncer(ctx, options.fromConfig, options.toConfig, sets.NewString(options.SyncedResourceTypes...), options.FromClusterName, options.PclusterID, numThreads); err != nil {
		return err
	}

	return nil
}

type Options struct {
	FromKubeconfig      string
	FromClusterName     string
	ToKubeconfig        string
	ToContext           string
	PclusterID          string
	Logs                *logs.Options
	SyncedResourceTypes []string

	//non-exported members
	toConfig   *rest.Config
	fromConfig *rest.Config
}

func NewDefaultOptions() *Options {
	return &Options{
		FromKubeconfig:      "",
		FromClusterName:     "",
		ToKubeconfig:        "",
		ToContext:           "",
		PclusterID:          "",
		SyncedResourceTypes: []string{"deployments.apps"},
		Logs:                logs.NewOptions(),
	}
}

func (options *Options) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.FromKubeconfig, "from-kubeconfig", options.FromKubeconfig, "Kubeconfig file for -from cluster.")
	fs.StringVar(&options.FromClusterName, "from-cluster", options.FromClusterName, "Name of the -from logical cluster.")
	fs.StringVar(&options.ToKubeconfig, "to-kubeconfig", options.ToKubeconfig, "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	fs.StringVar(&options.ToContext, "to-context", options.ToContext, "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	fs.StringVar(&options.PclusterID, "cluster", options.PclusterID,
		fmt.Sprintf("ID of the -to cluster. Resources with this ID set in the '%s' label will be synced.", nscontroller.ClusterLabel))
	fs.StringArrayVarP(&options.SyncedResourceTypes, "sync-resources", "r", options.SyncedResourceTypes, "Resources to be synchronized in kcp.")

	options.Logs.AddFlags(fs)
}
