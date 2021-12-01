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
	"context"
	"errors"
	"runtime"

	"github.com/spf13/pflag"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	"github.com/kcp-dev/kcp/config"
	apiresourceapi "github.com/kcp-dev/kcp/pkg/apis/apiresource"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster"
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
	fs.StringVar(&o.SyncerImage, "syncer_image", o.SyncerImage, "Syncer image to install on clusters")
	fs.BoolVar(&o.PullMode, "pull_mode", o.PullMode, "Deploy the syncer in registered physical clusters in POD, and have it sync resources from KCP")
	fs.BoolVar(&o.PushMode, "push_mode", o.PushMode, "If true, run syncer for each cluster from inside cluster controller")
	fs.BoolVar(&o.AutoPublishAPIs, "auto_publish_apis", o.AutoPublishAPIs, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
	fs.IntVar(&o.NumThreads, "cluster_controller_threads", o.NumThreads, "Number of threads to use for the cluster controller.")
	fs.StringSliceVar(&o.ResourcesToSync, "resources_to_sync", o.ResourcesToSync, "Provides the list of resources that should be synced from KCP logical cluster to underlying physical clusters")
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
		return errors.New("can't set both --push_mode and --pull_mode")
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

func (c *Config) Start(ctx context.Context) error {
	// Register CRDs in both the admin and user logical clusters
	requiredCrds := []metav1.GroupKind{
		{Group: apiresourceapi.GroupName, Kind: "apiresourceimports"},
		{Group: apiresourceapi.GroupName, Kind: "negotiatedapiresources"},
		{Group: clusterapi.GroupName, Kind: "clusters"},
	}
	for _, contextName := range []string{"admin", "user"} {
		logicalClusterConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return err
		}
		crdClient := apiextensionsv1client.NewForConfigOrDie(logicalClusterConfig).CustomResourceDefinitions()
		if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
			return err
		}
	}

	syncerMode := SyncerModeNone
	if c.PullMode {
		syncerMode = SyncerModePull
	}
	if c.PushMode {
		syncerMode = SyncerModePush
	}

	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(c.kubeconfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return err
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
		return err
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
		return err
	}

	c.kcpSharedInformerFactory.Start(ctx.Done())
	c.crdSharedInformerFactory.Start(ctx.Done())
	go clusterController.Start(ctx, c.NumThreads)
	go apiresourceController.Start(ctx, c.NumThreads)

	return nil
}
