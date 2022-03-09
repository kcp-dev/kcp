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
	"errors"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
)

// Options provides options that will drive the update of the current context
// on a user's KUBECONFIG based on actions done on KCP workspaces
type Options struct {
	KubectlOverrides *clientcmd.ConfigOverrides
	Scope            string
	// Temporary, until we have #640 merged, in which case it should be necessary anymore
	Port int
	genericclioptions.IOStreams
}

// NewOptions provides an instance of Options with default values
func NewOptions(streams genericclioptions.IOStreams) *Options {
	return &Options{
		KubectlOverrides: &clientcmd.ConfigOverrides{},
		IOStreams:        streams,
	}
}

// BindFlags binds the arguments common to all sub-commands,
// to the corresponding main command flags
func (o *Options) BindFlags(cmd *cobra.Command) {
	// We add only a subset of kubeconfig-related flags to the plugin.
	// All those with with LongName == "" will be ignored.
	kubectlConfigOverrideFlags := clientcmd.RecommendedConfigOverrideFlags("")
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientCertificate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientKey.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.Impersonate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ImpersonateGroups.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.AuthInfoName.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.ClusterName.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.Namespace.LongName = ""
	kubectlConfigOverrideFlags.Timeout.LongName = ""

	clientcmd.BindOverrideFlags(o.KubectlOverrides, cmd.PersistentFlags(), kubectlConfigOverrideFlags)

	cmd.PersistentFlags().StringVar(&o.Scope, "scope", "personal", `The 'personal' scope shows only the workspaces you personally own, with the name you gave them at creation.
	The 'all' scope returns all the workspaces you are allowed to see in the organization, with the disambiguated names they have inside the whole organization.`)

	cmd.PersistentFlags().IntVar(&o.Port, "port", 0, `overrides the port that will be used to point to the workspace directory server. Default port is the port of the currrent kube context server.`)
}

func (o *Options) Validate() error {
	if o.Scope != "personal" && o.Scope != "all" {
		return errors.New("The scope should be either 'personal' (default) or 'all'")
	}
	return nil
}
