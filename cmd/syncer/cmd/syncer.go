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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	synceroptions "github.com/kcp-dev/kcp/cmd/syncer/options"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

const numThreads = 2

func NewSyncerCommand() *cobra.Command {
	options := synceroptions.NewOptions()
	syncerCommand := &cobra.Command{
		Use:   "syncer",
		Short: "Synchronizes resources in `kcp` assigned to the clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Logs.ValidateAndApply(kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := options.Complete(); err != nil {
				return err
			}

			if err := options.Validate(); err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()
			if err := Run(options, ctx); err != nil {
				return err
			}

			<-ctx.Done()

			return nil
		},
	}

	options.AddFlags(syncerCommand.Flags())

	if v := version.Get().String(); len(v) == 0 {
		syncerCommand.Version = "<unknown>"
	} else {
		syncerCommand.Version = v
	}

	return syncerCommand
}

func Run(options *synceroptions.Options, ctx context.Context) error {
	klog.Infof("Syncing the following resource types: %s", options.SyncedResourceTypes)

	kcpConfigOverrides := &clientcmd.ConfigOverrides{
		CurrentContext: options.FromContext,
	}
	upstreamConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.FromKubeconfig},
		kcpConfigOverrides).ClientConfig()
	if err != nil {
		return err
	}

	upstreamConfig.QPS = options.QPS
	upstreamConfig.Burst = options.Burst

	downstreamConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.ToKubeconfig},
		&clientcmd.ConfigOverrides{
			CurrentContext: options.ToContext,
		}).ClientConfig()
	if err != nil {
		return err
	}

	downstreamConfig.QPS = options.QPS
	downstreamConfig.Burst = options.Burst

	if err := syncer.StartSyncer(
		ctx,
		&syncer.SyncerConfig{
			UpstreamConfig:      upstreamConfig,
			DownstreamConfig:    downstreamConfig,
			ResourcesToSync:     sets.NewString(options.SyncedResourceTypes...),
			SyncTargetWorkspace: logicalcluster.New(options.FromClusterName),
			SyncTargetName:      options.SyncTargetName,
			SyncTargetUID:       options.SyncTargetUID,
		},
		numThreads,
		options.APIImportPollInterval,
	); err != nil {
		return err
	}

	return nil
}
