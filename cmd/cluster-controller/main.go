package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

const resyncPeriod = 10 * time.Hour

func bindOptions(fs *pflag.FlagSet) *options {
	o := options{
		Options: cluster.BindOptions(fs),
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

	fs := pflag.NewFlagSet("cluster-controller", pflag.ExitOnError)
	options := bindOptions(fs)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		klog.Fatal(err)
	}
	if err := options.Validate(); err != nil {
		klog.Fatal(err)
	}

	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.kubeconfigPath},
		&clientcmd.ConfigOverrides{})

	r, err := configLoader.ClientConfig()
	if err != nil {
		klog.Fatal(err)
	}
	kubeconfig, err := configLoader.RawConfig()
	if err != nil {
		klog.Fatal(err)
	}
	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(r), resyncPeriod)
	crdSharedInformerFactory := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(r), resyncPeriod)
	if err := options.Options.Complete(kubeconfig, kcpSharedInformerFactory, crdSharedInformerFactory).Start(ctx); err != nil {
		klog.Fatal(err)
	}

	<-ctx.Done()
}
