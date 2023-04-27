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
	"time"

	kubernetesinformers "github.com/kcp-dev/client-go/informers"
	kubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	"github.com/spf13/cobra"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/version"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/reconciler/coordination/deployment"
	"github.com/kcp-dev/kcp/tmc/cmd/deployment-coordinator/options"
)

const numThreads = 2

const resyncPeriod = 10 * time.Hour

func NewDeploymentCoordinatorCommand() *cobra.Command {
	options := options.NewOptions()
	command := &cobra.Command{
		Use:   "deployment-coordinator",
		Short: "Coordination controller for deployments. Spreads replicas across locations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := logsapiv1.ValidateAndApply(options.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}
			if err := options.Complete(); err != nil {
				return err
			}

			if err := options.Validate(); err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()
			if err := Run(ctx, options); err != nil {
				return err
			}

			<-ctx.Done()

			return nil
		},
	}

	options.AddFlags(command.Flags())

	if v := version.Get().String(); len(v) == 0 {
		command.Version = "<unknown>"
	} else {
		command.Version = v
	}

	return command
}

func Run(ctx context.Context, options *options.Options) error {
	defaultLoadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	defaultLoadingRules.ExplicitPath = options.Kubeconfig
	r, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		defaultLoadingRules,
		&clientcmd.ConfigOverrides{
			CurrentContext: options.Context,
			ClusterInfo: api.Cluster{
				Server: options.Server,
			},
		}).ClientConfig()
	if err != nil {
		return err
	}

	kcpVersion := version.Get().GitVersion

	kcpCluterClient, err := kubernetesclient.NewForConfig(rest.AddUserAgent(rest.CopyConfig(r), "kcp#deployment-coordinator/"+kcpVersion))
	if err != nil {
		return err
	}

	kubeInformerFactory := kubernetesinformers.NewSharedInformerFactoryWithOptions(kcpCluterClient, resyncPeriod)

	controller, err := deployment.NewController(ctx, kcpCluterClient, kubeInformerFactory.Apps().V1().Deployments())
	if err != nil {
		return err
	}
	kubeInformerFactory.Start(ctx.Done())
	kubeInformerFactory.WaitForCacheSync(ctx.Done())

	controller.Start(ctx, numThreads)

	return nil
}
