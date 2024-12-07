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

package backend

import (
	"time"

	kubeinformers "github.com/kcp-dev/client-go/informers"
	kubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	bindclient "github.com/kube-bind/kube-bind/pkg/client/clientset/versioned"
	bindinformers "github.com/kube-bind/kube-bind/pkg/client/informers/externalversions"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/contrib/kube-bind/options"
)

type Config struct {
	Options *options.CompletedOptions

	ClientConfig        *rest.Config
	BindClient          *bindclient.Clientset
	KubeClient          *kubernetesclient.ClusterClientset
	ApiextensionsClient *apiextensionsclient.Clientset

	KubeInformers          kubeinformers.SharedInformerFactory
	BindInformers          bindinformers.SharedInformerFactory
	ApiextensionsInformers apiextensionsinformers.SharedInformerFactory
}

func NewConfig(options *options.CompletedOptions) (*Config, error) {
	config := &Config{
		Options: options,
	}

	// create clients
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = options.KubeConfig
	var err error
	config.ClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	config.ClientConfig = rest.CopyConfig(config.ClientConfig)
	config.ClientConfig = rest.AddUserAgent(config.ClientConfig, "kube-bind-kcp-backend")

	if config.BindClient, err = bindclient.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.KubeClient, err = kubernetesclient.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}
	if config.ApiextensionsClient, err = apiextensionsclient.NewForConfig(config.ClientConfig); err != nil {
		return nil, err
	}

	// construct informer factories
	config.KubeInformers = kubeinformers.NewSharedInformerFactory(config.KubeClient, time.Minute*30)
	config.BindInformers = bindinformers.NewSharedInformerFactory(config.BindClient, time.Minute*30)
	config.ApiextensionsInformers = apiextensionsinformers.NewSharedInformerFactory(config.ApiextensionsClient, time.Minute*30)

	return config, nil
}
