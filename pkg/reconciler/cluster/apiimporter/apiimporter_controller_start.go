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

package apiimporter

import (
	"github.com/spf13/pflag"

	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

func DefaultOptions() *Options {
	return &Options{
		ResourcesToSync: []string{"deployments.apps"},
	}
}

func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.StringSliceVar(&o.ResourcesToSync, "resources-to-sync", o.ResourcesToSync, "Provides the list of resources that should be synced from KCP logical cluster to underlying physical clusters")
	return o
}

type Options struct {
	ResourcesToSync []string
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

func (c *Config) New() (*clusterctl.ClusterReconciler, error) {
	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, "root", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	clientutils.EnableMultiCluster(adminConfig, nil, true, "clusters", "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")

	kcpClient := kcpclient.NewForConfigOrDie(adminConfig)

	return NewController(
		kcpClient,
		c.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters(),
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		c.ResourcesToSync,
	)

}
