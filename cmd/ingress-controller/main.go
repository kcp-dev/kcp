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

	"github.com/spf13/cobra"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/pkg/cmd/help"
	envoycontrolplane "github.com/kcp-dev/kcp/pkg/envoy-controlplane"
	"github.com/kcp-dev/kcp/pkg/reconciler/ingress"
)

const numThreads = 2
const resyncPeriod = 10 * time.Hour

func main() {
	help.FitTerminal()

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

			kubeconfig := cmd.Flag("kubeconfig").Value.String()
			kubecontext := cmd.Flag("context").Value.String()

			var overrides clientcmd.ConfigOverrides
			if kubecontext != "" {
				overrides.CurrentContext = kubecontext
			}

			configLoader, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
				&overrides).ClientConfig()
			if err != nil {
				return err
			}

			kubeClient, err := kubernetes.NewClusterForConfig(configLoader)
			if err != nil {
				return err
			}

			kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient.Cluster("*"), resyncPeriod)
			ingressInformer := kubeInformerFactory.Networking().V1().Ingresses()
			serviceInformer := kubeInformerFactory.Core().V1().Services()

			var ecp *envoycontrolplane.EnvoyControlPlane
			if cmd.Flag("envoyxds").Value.String() == "true" {
				envoyXDSPort, err := cmd.Flags().GetUint("envoyxds-port")
				if err != nil {
					return err
				}
				envoyListenPort, err := cmd.Flags().GetUint("envoy-listener-port")
				if err != nil {
					return err
				}
				ecp = envoycontrolplane.NewEnvoyControlPlane(envoyXDSPort, envoyListenPort, ingressInformer.Lister(), nil)
			}

			domain := cmd.Flag("domain").Value.String()
			ic := ingress.NewController(kubeClient, ingressInformer, serviceInformer, ecp, domain)
			kubeInformerFactory.Start(ctx.Done())
			kubeInformerFactory.WaitForCacheSync(ctx.Done())

			ic.Start(ctx, numThreads)

			<-ctx.Done()

			return nil
		},
	}

	//TODO(jmprusi): Use and options struct and use xyzVar() type for flag binding.
	cmd.Flags().String("kubeconfig", ".kubeconfig", "kubeconfig file used to contact the cluster.")
	cmd.Flags().String("context", "", "Context to use in the kubeconfig file, instead of the current context.")
	cmd.Flags().Bool("envoyxds", false, "Start an Envoy control plane")
	cmd.Flags().Uint("envoyxds-port", 18000, "Envoy control plane port")
	cmd.Flags().Uint("envoy-listener-port", 80, "Envoy default listener port")
	cmd.Flags().String("domain", "kcp-apps.127.0.0.1.nip.io", "The domain to use to expose ingresses")

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
