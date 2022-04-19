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
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type Config struct {
	startingConfig *clientcmdapi.Config
	overrides      *clientcmd.ConfigOverrides

	genericclioptions.IOStreams
}

// NewConfig load a kubeconfig with default config access
func NewConfig(opts *Options) (*Config, error) {
	configAccess := clientcmd.NewDefaultClientConfigLoadingRules()
	startingConfig, err := configAccess.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	return &Config{
		startingConfig: startingConfig,
		overrides:      opts.KubectlOverrides,

		IOStreams: opts.IOStreams,
	}, nil
}
