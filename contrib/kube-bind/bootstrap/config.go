/*
Copyright 2022 The Kube Bind Authors.

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

package bootstrap

import (
	"net/url"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/options"
)

type Config struct {
	Options *options.CompletedOptions

	ClientConfig         *rest.Config
	KcpClusterClient     kcpclusterclientset.ClusterInterface
	ApiextensionsClient  kcpapiextensionsclientset.ClusterInterface
	DynamicClusterClient kcpdynamic.ClusterInterface
}

func NewConfig(options *options.CompletedOptions) (*Config, error) {
	config := &Config{
		Options: options,
	}

	kcpClientConfigOverrides := &clientcmd.ConfigOverrides{
		CurrentContext: options.Context,
	}
	var err error
	config.ClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.KCPKubeConfig},
		kcpClientConfigOverrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	config.ClientConfig = rest.CopyConfig(config.ClientConfig)
	config.ClientConfig = rest.AddUserAgent(config.ClientConfig, "kube-bind-kcp-init")

	config.ClientConfig, err = newKCPRestConfig(config.ClientConfig)
	if err != nil {
		return nil, err
	}

	if config.KcpClusterClient, err = kcpclusterclientset.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.ApiextensionsClient, err = kcpapiextensionsclientset.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.DynamicClusterClient, err = kcpdynamic.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}

	return config, nil
}

func newKCPRestConfig(restConfig *rest.Config) (*rest.Config, error) {
	clusterConfig := rest.CopyConfig(restConfig)
	u, err := url.Parse(restConfig.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	return clusterConfig, nil
}
