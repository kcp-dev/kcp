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

package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/templates"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	virtualbuilders "github.com/kcp-dev/kcp/pkg/virtual/generic/builders"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/generic/rootapiserver"
)

type APIServerOptions struct {
	Output io.Writer

	SecureServing     *genericapiserveroptions.SecureServingOptionsWithLoopback
	Authentication    *genericapiserveroptions.DelegatingAuthenticationOptions
	SubCommandOptions SubCommandOptions
}

type SubCommandDescription struct {
	Name  string
	Use   string
	Short string
	Long  string
}

type SubCommandOptions interface {
	Description() SubCommandDescription
	AddFlags(flags *pflag.FlagSet)
	Validate() []error
	InitializeBuilders() ([]virtualrootapiserver.InformerStart, []virtualbuilders.VirtualWorkspaceBuilder, error)
}

func APIServerCommand(out, errout io.Writer, stopCh <-chan struct{}, subCommandOptions SubCommandOptions) *cobra.Command {
	options := &APIServerOptions{
		Output:            out,
		SecureServing:     kubeoptions.NewSecureServingOptions().WithLoopback(),
		Authentication:    genericapiserveroptions.NewDelegatingAuthenticationOptions(),
		SubCommandOptions: subCommandOptions,
	}

	cmd := &cobra.Command{
		Use:   options.SubCommandOptions.Description().Use,
		Short: options.SubCommandOptions.Description().Short,
		Long:  templates.LongDesc(options.SubCommandOptions.Description().Long),
		Run: func(c *cobra.Command, args []string) {
			kcmdutil.CheckErr(options.Validate())

			if err := options.RunAPIServer(stopCh); err != nil {
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

	options.AddFlags(cmd.Flags())

	return cmd
}

func (o *APIServerOptions) AddFlags(flags *pflag.FlagSet) {
	o.SecureServing.AddFlags(flags)
	o.Authentication.AddFlags(flags)
	o.SubCommandOptions.AddFlags(flags)
}

func (o *APIServerOptions) Validate() error {
	errs := []error{}
	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.SubCommandOptions.Validate()...)
	return utilerrors.NewAggregate(errs)
}

func ReadKubeConfig(kubeConfigFile string) (clientcmd.ClientConfig, error) {
	// Resolve relative to CWD
	absoluteKubeConfigFile, err := api.MakeAbs(kubeConfigFile, "")
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

// RunAPIServer takes the options, starts the API server and waits until stopCh is closed or initial listening fails.
func (o *APIServerOptions) RunAPIServer(stopCh <-chan struct{}) error {
	informerStarts, virtualWorkspaceBuilders, err := o.SubCommandOptions.InitializeBuilders()
	if err != nil {
		return err
	}

	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(o.SecureServing, o.Authentication, informerStarts, virtualWorkspaceBuilders...)
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
