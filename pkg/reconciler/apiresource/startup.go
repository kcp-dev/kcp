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

package apiresource

import (
	"runtime"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

// DefaultOptions are the default options for the apiresource controller.
func DefaultOptions() *Options {
	return &Options{
		AutoPublishAPIs: false,
		// Consumed by server instantiation
		NumThreads: runtime.NumCPU(),
	}
}

// BindOptions binds the apiresource controller options to the flag set.
func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.BoolVar(&o.AutoPublishAPIs, "auto-publish-apis", o.AutoPublishAPIs, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
	fs.IntVar(&o.NumThreads, "apiresource-controller-threads", o.NumThreads, "Number of threads to use for the cluster controller.")
	return o
}

// Options are the options for the cluster controller
type Options struct {
	AutoPublishAPIs bool
	NumThreads      int
}

func (o *Options) Validate() error {
	return nil
}

func (o *Options) Complete(kubeconfig clientcmdapi.Config, kcpSharedInformerFactory kcpexternalversions.SharedInformerFactory, crdSharedInformerFactory crdexternalversions.SharedInformerFactory) *Config {
	return &Config{
		Options:                  o,
		kubeconfig:               kubeconfig,
		kcpSharedInformerFactory: kcpSharedInformerFactory,
		crdSharedInformerFactory: crdSharedInformerFactory,
	}
}

type Config struct {
	*Options
	kubeconfig               clientcmdapi.Config
	kcpSharedInformerFactory kcpexternalversions.SharedInformerFactory
	crdSharedInformerFactory crdexternalversions.SharedInformerFactory
}

func (c *Config) New() (*Controller, error) {
	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, "root", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	clientutils.EnableMultiCluster(adminConfig, nil, true, "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")

	apiExtensionsClient := apiextensionsclient.NewForConfigOrDie(adminConfig)

	neutralConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, "system:admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	kcpClusterClient, err := kcpclient.NewClusterForConfig(neutralConfig)
	if err != nil {
		return nil, err
	}

	return NewController(
		apiExtensionsClient,
		kcpClusterClient,
		c.AutoPublishAPIs,
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		c.crdSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
}
