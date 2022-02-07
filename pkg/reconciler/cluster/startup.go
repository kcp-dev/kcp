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

package cluster

import (
	"errors"
	"runtime"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
)

// DefaultOptions are the default options for the cluster controller.
func DefaultOptions() *Options {
	return &Options{
		SyncerImage:     "",
		PullMode:        false,
		PushMode:        false,
		AutoPublishAPIs: false,
		NumThreads:      runtime.NumCPU(),
		ResourcesToSync: []string{"deployments.apps"},
	}
}

// BindOptions binds the cluster controller options to the flag set.
func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.StringVar(&o.SyncerImage, "syncer-image", o.SyncerImage, "Syncer image to install on clusters")
	fs.BoolVar(&o.PullMode, "pull-mode", o.PullMode, "Deploy the syncer in registered physical clusters in POD, and have it sync resources from KCP")
	fs.BoolVar(&o.PushMode, "push-mode", o.PushMode, "If true, run syncer for each cluster from inside cluster controller")
	fs.BoolVar(&o.AutoPublishAPIs, "auto-publish-apis", o.AutoPublishAPIs, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
	fs.IntVar(&o.NumThreads, "cluster-controller-threads", o.NumThreads, "Number of threads to use for the cluster controller.")
	fs.StringSliceVar(&o.ResourcesToSync, "resources-to-sync", o.ResourcesToSync, "Provides the list of resources that should be synced from KCP logical cluster to underlying physical clusters")
	return o
}

// Options are the options for the cluster controller
type Options struct {
	SyncerImage     string
	PullMode        bool
	PushMode        bool
	AutoPublishAPIs bool
	NumThreads      int
	ResourcesToSync []string
}

func (o *Options) Validate() error {
	if o.PullMode && o.PushMode {
		return errors.New("can't set both --push-mode and --pull-mode")
	}
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

func (c *Config) New() (*Controller, *apiresource.Controller, error) {
	syncerMode := SyncerModeNone
	if c.PullMode {
		syncerMode = SyncerModePull
	}
	if c.PushMode {
		syncerMode = SyncerModePush
	}

	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	clientutils.EnableMultiCluster(adminConfig, nil, true, "clusters", "customresourcedefinitions", "apiresourceimports", "negotiatedapiresources")

	apiExtensionsClient := apiextensionsclient.NewForConfigOrDie(adminConfig)
	kcpClient := kcpclient.NewForConfigOrDie(adminConfig)

	clusterController, err := NewController(
		apiExtensionsClient,
		kcpClient,
		c.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters(),
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		c.SyncerImage,
		c.kubeconfig,
		c.ResourcesToSync,
		syncerMode,
	)
	if err != nil {
		return nil, nil, err
	}

	apiresourceController, err := apiresource.NewController(
		apiExtensionsClient,
		kcpClient,
		c.AutoPublishAPIs,
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		c.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		c.crdSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return nil, nil, err
	}

	return clusterController, apiresourceController, nil
}
