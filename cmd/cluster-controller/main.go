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
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config"
	apiresourceapi "github.com/kcp-dev/kcp/pkg/apis/apiresource"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

const resyncPeriod = 10 * time.Hour

func bindOptions(fs *pflag.FlagSet) *options {
	o := options{
		Options: cluster.BindOptions(cluster.DefaultOptions(), fs),
	}
	fs.StringVar(&o.kubeconfigPath, "kubeconfig", "", "Path to kubeconfig")
	return &o
}

type options struct {
	// in the all-in-one startup, client credentials already exist; in this
	// standalone startup, we need to load credentials ourselves
	kubeconfigPath string
	*cluster.Options
}

func (o *options) Validate() error {
	if o.kubeconfigPath == "" {
		return errors.New("--kubeconfig is required")
	}
	return o.Options.Validate()
}

func main() {
	// Setup signal handler for a cleaner shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt)
	defer cancel()

	fs := pflag.NewFlagSet("cluster-controller", pflag.ContinueOnError)
	options := bindOptions(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := options.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.kubeconfigPath},
		&clientcmd.ConfigOverrides{})

	r, err := configLoader.ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	kubeconfig, err := configLoader.RawConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(r), resyncPeriod)
	crdSharedInformerFactory := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(r), resyncPeriod)
	c := options.Options.Complete(kubeconfig, kcpSharedInformerFactory, crdSharedInformerFactory)
	cluster, apiresource, err := c.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	kcpSharedInformerFactory.Start(ctx.Done())
	crdSharedInformerFactory.Start(ctx.Done())

	kcpSharedInformerFactory.WaitForCacheSync(ctx.Done())
	crdSharedInformerFactory.WaitForCacheSync(ctx.Done())

	// TODO(sttts): remove CRD creation from controller startup
	requiredCrds := []metav1.GroupResource{
		{Group: apiresourceapi.GroupName, Resource: "apiresourceimports"},
		{Group: apiresourceapi.GroupName, Resource: "negotiatedapiresources"},
		{Group: clusterapi.GroupName, Resource: "clusters"},
	}
	for _, contextName := range []string{"admin", "user"} {
		logicalClusterConfig, err := clientcmd.NewNonInteractiveClientConfig(kubeconfig, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		crdClient := apiextensionsv1client.NewForConfigOrDie(logicalClusterConfig).CustomResourceDefinitions()
		if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	prepared, err := cluster.Prepare()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go prepared.Start(ctx)
	go apiresource.Start(ctx, c.NumThreads)

	<-ctx.Done()
}
