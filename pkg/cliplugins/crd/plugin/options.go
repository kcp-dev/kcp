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
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
)

// Options for the crd commands.
type Options struct {
	KubectlOverrides *clientcmd.ConfigOverrides

	genericclioptions.IOStreams

	Filename     string
	Prefix       string
	OutputFormat string
}

// NewOptions provides an instance of Options with default values
func NewOptions(streams genericclioptions.IOStreams) *Options {
	return &Options{
		KubectlOverrides: &clientcmd.ConfigOverrides{},
		IOStreams:        streams,
		OutputFormat:     "yaml",
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

	cmd.Flags().StringVarP(&o.Filename, "filename", "f", o.Filename, "Path to a file containing the CRD to convert to an APIResourceSchema, or - for stdin")
	cmd.Flags().StringVar(&o.Prefix, "prefix", o.Prefix, "Prefix to use for the APIResourceSchema's name, before <resource>.<group>")
	cmd.Flags().StringVarP(&o.OutputFormat, "output", "o", o.OutputFormat, "Output format. Valid values are 'json' and 'yaml'")
}

func (o *Options) Validate() error {
	var errs []error

	if o.Filename == "" {
		errs = append(errs, fmt.Errorf("--filename is required"))
	}

	if o.Prefix == "" {
		errs = append(errs, fmt.Errorf("--prefix is required"))
	}

	if o.OutputFormat != "json" && o.OutputFormat != "yaml" {
		errs = append(errs, fmt.Errorf("invalid value %q for --output; valid values are json, yaml", o.OutputFormat))
	}

	return errors.NewAggregate(errs)
}
