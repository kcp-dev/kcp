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

package cluster

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/tools/clientcmd"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

const resyncPeriod = 10 * time.Hour

type CmdOptions struct {
	// in the all-in-one startup, client credentials already exist; in this
	// standalone startup, we need to load credentials ourselves
	KubeConfigPath string
	*Options
}

func BindCmdOptions(fs *pflag.FlagSet) *CmdOptions {
	o := CmdOptions{
		Options: BindOptions(DefaultOptions(), fs),
	}
	fs.StringVar(&o.KubeConfigPath, "kubeconfig", "", "Path to kubeconfig")
	return &o
}

func (o *CmdOptions) Validate() error {
	if o.KubeConfigPath == "" {
		return errors.New("--kubeconfig is required")
	}
	return o.Options.Validate()
}

func StartController(ctx context.Context, options *CmdOptions) error {
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.KubeConfigPath},
		&clientcmd.ConfigOverrides{})

	r, err := configLoader.ClientConfig()
	if err != nil {
		return err
	}
	kubeconfig, err := configLoader.RawConfig()
	if err != nil {
		return err
	}

	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(r), resyncPeriod)
	crdSharedInformerFactory := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(r), resyncPeriod)
	c := options.Options.Complete(kubeconfig, kcpSharedInformerFactory, crdSharedInformerFactory)
	cluster, apiresource, err := c.New()
	if err != nil {
		return err
	}

	kcpSharedInformerFactory.Start(ctx.Done())
	crdSharedInformerFactory.Start(ctx.Done())

	kcpSharedInformerFactory.WaitForCacheSync(ctx.Done())
	crdSharedInformerFactory.WaitForCacheSync(ctx.Done())

	prepared, err := cluster.Prepare()
	if err != nil {
		return err
	}
	go prepared.Start(ctx)
	go apiresource.Start(ctx, c.NumThreads)

	return nil
}
