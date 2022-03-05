/*
Copyright 2021 The KCP Authors.

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

package command

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

type SubCommandOptions interface {
	NewVirtualWorkspaces() ([]virtualrootapiserver.InformerStart, []framework.VirtualWorkspace, error)
}

func NewCommand(out, errout io.Writer, stopCh <-chan struct{}) *cobra.Command {
	opts := options.NewOptions()
	cmd := &cobra.Command{
		Use:   "workspaces",
		Short: "Launch workspaces virtual workspace apiserver",
		Long:  "Start a virtual workspace apiserver to managing personal, shared or organization workspaces",

		Run: func(c *cobra.Command, args []string) {
			kcmdutil.CheckErr(opts.Validate())

			if err := Run(opts, stopCh); err != nil {
				if kerrors.IsInvalid(err) {
					var statusError *kerrors.StatusError
					if isStatusError := errors.As(err, &statusError); isStatusError && statusError.ErrStatus.Details != nil {
						details := statusError.ErrStatus.Details
						fmt.Fprintf(errout, "Invalid %s %s\n", details.Kind, details.Name)
						for _, cause := range details.Causes {
							fmt.Fprintf(errout, "  %s: %s\n", cause.Field, cause.Message)
						}
						os.Exit(255)
					}
				}
				klog.Fatal(err)
			}
		},
	}

	opts.AddFlags(cmd.Flags())

	return cmd
}

// Run takes the options, starts the API server and waits until stopCh is closed or initial listening fails.
func Run(o *options.Options, stopCh <-chan struct{}) error {
	// parse kubeconfig
	kubeConfig, err := readKubeConfig(o.Workspaces.KubeconfigFile)
	if err != nil {
		return err
	}
	kubeClientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return err
	}
	u, err := url.Parse(kubeClientConfig.Host)
	if err != nil {
		return err
	}
	u.Path = ""
	kubeClientConfig.Host = u.String()

	// create clients and informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return err
	}
	wildcardKubeClient := kubeClusterClient.Cluster("*")
	wildcardKubeInformers := kubeinformers.NewSharedInformerFactory(wildcardKubeClient, 10*time.Minute)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(kubeClientConfig)
	if err != nil {
		return err
	}
	wildcardKcpClient := kcpClusterClient.Cluster("*")
	wildcardKcpInformers := kcpinformer.NewSharedInformerFactory(wildcardKcpClient, 10*time.Minute)

	// create apiserver
	extraInformerStarts, virtualWorkspaces, err := o.Workspaces.NewVirtualWorkspaces(kubeClusterClient, kcpClusterClient, wildcardKubeInformers, wildcardKcpInformers)
	if err != nil {
		return err
	}
	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(&o.SecureServing, &o.Authentication, append(extraInformerStarts,
		wildcardKubeInformers.Start,
		wildcardKcpInformers.Start,
	), virtualWorkspaces...)
	if err != nil {
		return err
	}

	completedRootAPIServerConfig := rootAPIServerConfig.Complete()
	rootAPIServer, err := completedRootAPIServerConfig.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}
	preparedRootAPIServer := rootAPIServer.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := completedRootAPIServerConfig.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return err
	}

	klog.Infof("Starting virtual workspace apiserver on %s (%s)", rootAPIServerConfig.GenericConfig.ExternalAddress, version.Get().String())

	return preparedRootAPIServer.Run(stopCh)
}

func readKubeConfig(kubeConfigFile string) (clientcmd.ClientConfig, error) {
	// Resolve relative to CWD
	absoluteKubeConfigFile, err := clientcmdapi.MakeAbs(kubeConfigFile, "")
	if err != nil {
		return nil, err
	}

	kubeConfigBytes, err := ioutil.ReadFile(absoluteKubeConfigFile)
	if err != nil {
		return nil, err
	}
	kubeConfig, err := clientcmd.NewClientConfigFromBytes(kubeConfigBytes)
	if err != nil {
		return nil, err
	}
	return kubeConfig, nil
}
