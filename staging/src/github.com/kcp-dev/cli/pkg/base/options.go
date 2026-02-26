/*
Copyright 2022 The kcp Authors.

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

package base

import (
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
)

// Options contains options common to most CLI plugins, including settings for connecting to kcp (kubeconfig, etc).
type Options struct {
	// OptOutOfDefaultKubectlFlags indicates that the standard kubectl/kubeconfig-related flags should not be bound
	// by default.
	OptOutOfDefaultKubectlFlags bool
	// Kubeconfig specifies kubeconfig file(s).
	Kubeconfig string
	// KubectlOverrides stores the extra client connection fields, such as context, user, etc.
	KubectlOverrides *clientcmd.ConfigOverrides

	genericclioptions.IOStreams

	// ClientConfig is the resolved cliendcmd.ClientConfig based on the client connection flags. This is only valid
	// after calling Complete.
	ClientConfig clientcmd.ClientConfig
}

// NewOptions provides an instance of Options with default values.
func NewOptions(streams genericclioptions.IOStreams) *Options {
	return &Options{
		KubectlOverrides: &clientcmd.ConfigOverrides{},
		IOStreams:        streams,
	}
}

// BindFlags binds options fields to cmd's flagset.
func (o *Options) BindFlags(cmd *cobra.Command) {
	if o.OptOutOfDefaultKubectlFlags {
		return
	}

	cmd.Flags().StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "path to the kubeconfig file")

	// We add only a subset of kubeconfig-related flags to the plugin.
	// All those with LongName == "" will be ignored.
	kubectlConfigOverrideFlags := clientcmd.RecommendedConfigOverrideFlags("")
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientCertificate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientKey.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.Impersonate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ImpersonateGroups.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.ClusterName.LongName = ""
	kubectlConfigOverrideFlags.Timeout.LongName = ""

	clientcmd.BindOverrideFlags(o.KubectlOverrides, cmd.PersistentFlags(), kubectlConfigOverrideFlags)
}

// Complete initializes ClientConfig based on Kubeconfig and KubectlOverrides.
func (o *Options) Complete() error {
	if o.ClientConfig == nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = o.Kubeconfig

		o.ClientConfig = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, o.KubectlOverrides)
	}

	return nil
}

// Validate validates the configured options.
func (o *Options) Validate() error {
	return nil
}
