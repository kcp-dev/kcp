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

package main

import (
	"fmt"
	"os"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"
	kubernetesinformers "k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/config"
	"k8s.io/component-base/logs"

	"github.com/kcp-dev/kcp/pkg/cmd/help"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/localenvoy/controllers/ingress"
	envoycontrolplane "github.com/kcp-dev/kcp/pkg/localenvoy/controlplane"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/ingresssplitter"
)

const numThreads = 2
const resyncPeriod = 10 * time.Hour

func main() {
	options := NewDefaultOptions()

	cmd := &cobra.Command{
		Use:   "ingress-controller",
		Short: "KCP ingress controller",
		Long: help.Doc(`
					KCP ingress controller.

					Transparently synchronizes ingresses from kcp to physical clusters.

					Has an optional Envoy XDS control plane that programs Envoy based on
					ingresses in kcp.
				`),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := genericapiserver.SetupSignalContext()

			if err := options.Logs.ValidateAndApply(kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}

			var overrides clientcmd.ConfigOverrides
			if options.Context != "" {
				overrides.CurrentContext = options.Context
			}

			// Supports standard env vars for e.g. KUBECONFIG
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			// Use the user-supplied kubeconfig, if specified
			loadingRules.ExplicitPath = options.Kubeconfig

			configLoader, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &overrides).ClientConfig()
			if err != nil {
				return err
			}

			kubeClient, err := kubernetesclient.NewForConfig(configLoader)
			if err != nil {
				return err
			}

			kubeInformerConfig := kcpclienthelper.SetCluster(rest.CopyConfig(configLoader), logicalcluster.Wildcard)
			kubeInformerClient, err := kubernetesclient.NewForConfig(kubeInformerConfig)
			if err != nil {
				return err
			}

			kubeInformerFactory := kubernetesinformers.NewSharedInformerFactory(kubeInformerClient, resyncPeriod)
			ingressInformer := kubeInformerFactory.Networking().V1().Ingresses()
			serviceInformer := kubeInformerFactory.Core().V1().Services()

			var ecp *envoycontrolplane.EnvoyControlPlane
			aggregateLeavesStatus := true
			if options.EnvoyXDSPort > 0 && options.EnvoyListenerPort > 0 {
				aggregateLeavesStatus = false

				ecp = envoycontrolplane.NewEnvoyControlPlane(options.EnvoyXDSPort, options.EnvoyListenerPort, ingressInformer.Lister(), nil)
				isr := ingress.NewController(kubeClient, ingressInformer, ecp, options.Domain)
				go isr.Start(ctx, numThreads)
				if err := ecp.Start(ctx); err != nil {
					return err
				}
			}

			ic := ingresssplitter.NewController(ingressInformer, serviceInformer, options.Domain, aggregateLeavesStatus)

			kubeInformerFactory.Start(ctx.Done())
			kubeInformerFactory.WaitForCacheSync(ctx.Done())

			ic.Start(ctx, numThreads)

			<-ctx.Done()

			return nil
		},
	}

	options.BindFlags(cmd.Flags())

	help.FitTerminal(cmd.OutOrStdout())

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}

type Options struct {
	Kubeconfig        string
	Context           string
	EnvoyXDSPort      uint
	EnvoyListenerPort uint
	Domain            string
	Logs              *logs.Options
}

func NewDefaultOptions() *Options {
	// Default to -v=2
	logs := logs.NewOptions()
	logs.Config.Verbosity = config.VerbosityLevel(2)

	return &Options{
		Kubeconfig:        "",
		Context:           "",
		EnvoyXDSPort:      18000,
		EnvoyListenerPort: 80,
		Domain:            "kcp-apps.127.0.0.1.nip.io",
		Logs:              logs,
	}
}

func (o *Options) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "kubeconfig file used to contact the cluster")
	fs.StringVar(&o.Context, "context", o.Context, "Context to use in the kubeconfig file, instead of the current context")
	fs.UintVar(&o.EnvoyXDSPort, "envoy-xds-port", o.EnvoyXDSPort, "Envoy control plane port. Set to 0 to disable")
	fs.UintVar(&o.EnvoyListenerPort, "envoy-listener-port", o.EnvoyListenerPort, "Envoy listener port")
	fs.StringVar(&o.Domain, "domain", o.Domain, "The domain to use to expose ingresses")

	o.Logs.AddFlags(fs)
}
