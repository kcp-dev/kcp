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

package plugin

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
)

// CreateContextOptions contains options for creating or updating a kubeconfig context.
type CreateContextOptions struct {
	*base.Options

	// Name is the name of the context to create.
	Name string
	// Overwrite indicates the context should be updated if it already exists. This is required to perform the update.
	Overwrite bool

	// KeepCurrent indicates whether to keep the current context. When creating a new context, if KeepCurrent is true, the current context will be preserved.
	KeepCurrent bool

	// ClusterURL is the URL of the cluster to use for the new context.
	// If empty, the current context's cluster will be used.
	ClusterURL string

	startingConfig *clientcmdapi.Config

	// for testing
	modifyConfig func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
}

// NewCreateContextOptions returns a new CreateContextOptions.
func NewCreateContextOptions(streams genericclioptions.IOStreams) *CreateContextOptions {
	return &CreateContextOptions{
		Options: base.NewOptions(streams),

		modifyConfig: func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},
	}
}

// BindFlags binds fields to cmd's flagset.
func (o *CreateContextOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVar(&o.Overwrite, "overwrite", o.Overwrite, "Overwrite the context if it already exists")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *CreateContextOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	// Get the starting config if it hasn't been set yet.
	if o.startingConfig == nil {
		var err error
		o.startingConfig, err = o.ClientConfig.ConfigAccess().GetStartingConfig()
		if err != nil {
			return err
		}
	}

	if o.Name == "" && len(args) > 0 {
		o.Name = args[0]
	}

	return nil
}

// Validate validates the CreateContextOptions are complete and usable.
func (o *CreateContextOptions) Validate() error {
	return o.Options.Validate()
}

// Run creates or updates a kubeconfig context from the current context.
func (o *CreateContextOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.RawConfig()
	if err != nil {
		return err
	}
	currentContext, ok := config.Contexts[config.CurrentContext]
	if !ok {
		return fmt.Errorf("current context %q is not found in kubeconfig", config.CurrentContext)
	}
	currentCluster, ok := config.Clusters[currentContext.Cluster]
	if !ok {
		return fmt.Errorf("current cluster %q is not found in kubeconfig", currentContext.Cluster)
	}
	_, currentClusterName, err := pluginhelpers.ParseClusterURL(currentCluster.Server)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to a workspace", currentCluster.Server)
	}

	if o.Name == "" {
		o.Name = currentClusterName.String()
	}

	_, existedBefore := o.startingConfig.Contexts[o.Name]
	if existedBefore && !o.Overwrite {
		return fmt.Errorf("context %q already exists in kubeconfig, use --overwrite to update it", o.Name)
	}

	newKubeConfig := o.startingConfig.DeepCopy()
	newCluster := *currentCluster
	if o.ClusterURL != "" {
		newCluster.Server = o.ClusterURL
	}
	newKubeConfig.Clusters[o.Name] = &newCluster
	newContext := *currentContext
	newContext.Cluster = o.Name
	newKubeConfig.Contexts[o.Name] = &newContext
	newKubeConfig.CurrentContext = o.Name

	// If KeepCurrent is true, preserve the current context.
	if o.KeepCurrent {
		newKubeConfig.CurrentContext = config.CurrentContext
	}

	if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
		return err
	}

	verb := "Created"
	if existedBefore {
		verb = "Updated"
	}

	if o.KeepCurrent || o.startingConfig.CurrentContext == o.Name {
		_, err = fmt.Fprintf(o.Out, "%s context %q.\n", verb, o.Name)
	} else {
		_, err = fmt.Fprintf(o.Out, "%s context %q and switched to it.\n", verb, o.Name)
	}

	return err
}
