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

package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"

	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	kcpapiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/kcp/informers/externalversions"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource"
	apiresourceoptions "github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource/options"
)

const resyncPeriod = 10 * time.Hour

func NewOptions() *options {
	o := options{
		ApiResourceOptions: apiresourceoptions.NewOptions(),
	}
	return &o
}

type options struct {
	// in the all-in-one startup, client credentials already exist; in this
	// standalone startup, we need to load credentials ourselves
	kubeconfigPath string

	ApiResourceOptions *apiresourceoptions.Options
}

func (o *options) AddFlags(fs *pflag.FlagSet) {
	o.ApiResourceOptions.AddFlags(fs)

	fs.StringVar(&o.kubeconfigPath, "kubeconfig", "", "Path to kubeconfig")
}

func (o *options) Validate() error {
	if o.kubeconfigPath == "" {
		return errors.New("--kubeconfig is required")
	}
	return o.ApiResourceOptions.Validate()
}

func main() {
	// Setup signal handler for a cleaner shutdown
	ctx := genericapiserver.SetupSignalContext()

	fs := pflag.NewFlagSet("cluster-controller", pflag.ContinueOnError)
	o := NewOptions()
	o.AddFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := o.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.kubeconfigPath},
		&clientcmd.ConfigOverrides{})

	config, err := configLoader.ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	kcpSharedInformerFactory := kcpinformers.NewSharedInformerFactoryWithOptions(kcpClusterClient, resyncPeriod)
	crdSharedInformerFactory := kcpapiextensionsinformers.NewSharedInformerFactoryWithOptions(crdClusterClient, resyncPeriod)

	apiResource, err := apiresource.NewController(
		crdClusterClient,
		kcpClusterClient,
		o.ApiResourceOptions.AutoPublishAPIs,
		kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		crdSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	kcpSharedInformerFactory.Start(ctx.Done())
	crdSharedInformerFactory.Start(ctx.Done())

	kcpSharedInformerFactory.WaitForCacheSync(ctx.Done())
	crdSharedInformerFactory.WaitForCacheSync(ctx.Done())

	go apiResource.Start(ctx, o.ApiResourceOptions.NumThreads)

	<-ctx.Done()
}
