/*
Copyright 2023 The KCP Authors.

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

package manager

import (
	"context"
	"fmt"
	"time"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	proxyclusterclientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/cluster"
	proxyinformers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

/*
The manager is in charge of instantiating and starting the controllers
that do the reconciliation for Proxy.
*/

const (
	apiExportName = "proxy.kcp.io"
	resyncPeriod  = 10 * time.Hour
)

type Manager struct {
	*Config
	clientConfig *rest.Config

	proxySharedInformerFactory      proxyinformers.SharedInformerFactory
	cacheProxySharedInformerFactory proxyinformers.SharedInformerFactory

	kcpSharedInformerFactory kcpinformers.SharedInformerFactory
	//cacheKcpSharedInformerFactory kcpinformers.SharedInformerFactory

	kubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory

	// Virtual workspace server
	virtualWorkspaces []rootapiserver.NamedVirtualWorkspace

	stopCh   chan struct{}
	syncedCh chan struct{}
}

// NewManager creates a manager able to start controllers
func NewManager(ctx context.Context, cfg *Config, bootstrapClientConfig, cacheClientConfig *rest.Config) (*Manager, error) {
	var err error
	m := &Manager{
		Config:   cfg,
		stopCh:   make(chan struct{}),
		syncedCh: make(chan struct{}),
	}

	if m.clientConfig, err = restConfigForAPIExport(ctx, bootstrapClientConfig, apiExportName); err != nil {
		return nil, err
	}

	informerProxyClient, err := proxyclusterclientset.NewForConfig(m.clientConfig)
	if err != nil {
		return nil, err
	}
	cacheProxyClusterClient, err := proxyclusterclientset.NewForConfig(m.clientConfig)
	if err != nil {
		return nil, err
	}

	m.proxySharedInformerFactory = proxyinformers.NewSharedInformerFactoryWithOptions(
		informerProxyClient,
		resyncPeriod,
	)
	m.cacheProxySharedInformerFactory = proxyinformers.NewSharedInformerFactoryWithOptions(
		cacheProxyClusterClient,
		resyncPeriod,
	)

	informerKcpClient, err := kcpclusterclientset.NewForConfig(m.clientConfig)
	if err != nil {
		return nil, err
	}
	m.kcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
		informerKcpClient,
		resyncPeriod,
	)

	informerKubeClient, err := kcpkubernetesclientset.NewForConfig(m.clientConfig)
	if err != nil {
		return nil, err
	}
	m.kubeSharedInformerFactory = kcpkubernetesinformers.NewSharedInformerFactoryWithOptions(
		informerKubeClient,
		resyncPeriod,
	)

	// add proxy virtual workspaces
	virtualWorkspacesConfig := rest.CopyConfig(m.clientConfig)
	virtualWorkspacesConfig = rest.AddUserAgent(virtualWorkspacesConfig, "virtual-workspaces")

	m.virtualWorkspaces, err = m.Config.Options.ProxyVirtualWorkspaces.NewVirtualWorkspaces(
		m.Config.Options.VirtualWorkspaces.RootPathPrefix,
		virtualWorkspacesConfig,
		m.cacheProxySharedInformerFactory,
	)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// TODO (FGI): Need to define config.go with a Config struct
// including multiple sub-configs, one for each controller.
// they will get populated with NewConfig having Options as parameters

// Start starts informers and controller instances
func (m Manager) Start(ctx context.Context) error {

	logger := klog.FromContext(ctx)
	logger.V(2).Info("starting manager")

	if err := m.installWorkspaceProxyController(ctx); err != nil {
		return err
	}

	logger.Info("starting kube informers")
	m.kubeSharedInformerFactory.Start(m.stopCh)
	m.kcpSharedInformerFactory.Start(m.stopCh)
	//m.cacheKcpSharedInformerFactory.Start(m.stopCh)
	m.proxySharedInformerFactory.Start(m.stopCh)
	m.cacheProxySharedInformerFactory.Start(m.stopCh)

	logger.Info("waiting for kube informers sync")
	m.kubeSharedInformerFactory.WaitForCacheSync(m.stopCh)
	// This is not syncing. Missing something in layered config?
	//m.cacheKcpSharedInformerFactory.WaitForCacheSync(m.stopCh)
	m.kcpSharedInformerFactory.WaitForCacheSync(m.stopCh)
	m.proxySharedInformerFactory.WaitForCacheSync(m.stopCh)
	m.cacheProxySharedInformerFactory.WaitForCacheSync(m.stopCh)

	logger.V(2).Info("all informers synced, ready to start controllers")
	close(m.syncedCh)
	return nil
}

// restConfigForAPIExport returns a *rest.Config properly configured to communicate with the endpoint for the
// APIExport's virtual workspace.
func restConfigForAPIExport(ctx context.Context, cfg *rest.Config, apiExportName string) (*rest.Config, error) {

	logger := klog.FromContext(ctx)
	logger.V(2).Info("getting apiexport")

	bootstrapConfig := rest.CopyConfig(cfg)
	proxyVersion := version.Get().GitVersion
	rest.AddUserAgent(bootstrapConfig, "kcp#proxy/bootstrap/"+proxyVersion)
	bootstrapClient, err := kcpclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return nil, err
	}

	var apiExport *apisv1alpha1.APIExport

	if apiExportName != "" {
		if apiExport, err = bootstrapClient.ApisV1alpha1().APIExports().Get(ctx, apiExportName, metav1.GetOptions{}); err != nil {
			return nil, fmt.Errorf("error getting APIExport %q: %w", apiExportName, err)
		}
	} else {
		logger := klog.FromContext(ctx)
		logger.V(2).Info("api-export-name is empty - listing")
		exports := &apisv1alpha1.APIExportList{}
		if exports, err = bootstrapClient.ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{}); err != nil {
			return nil, fmt.Errorf("error listing APIExports: %w", err)
		}
		if len(exports.Items) == 0 {
			return nil, fmt.Errorf("no APIExport found")
		}
		if len(exports.Items) > 1 {
			return nil, fmt.Errorf("more than one APIExport found")
		}
		apiExport = &exports.Items[0]
	}

	if len(apiExport.Status.VirtualWorkspaces) < 1 {
		return nil, fmt.Errorf("APIExport %q status.virtualWorkspaces is empty", apiExportName)
	}

	cfg = rest.CopyConfig(cfg)
	// TODO(FGI): For sharding support we would need to interact with the APIExportEndpointSlice API
	// rather than APIExport. We would then have an URL per shard.
	cfg.Host = apiExport.Status.VirtualWorkspaces[0].URL

	return cfg, nil
}
