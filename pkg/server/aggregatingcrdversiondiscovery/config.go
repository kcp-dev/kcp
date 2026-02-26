/*
Copyright 2025 The kcp Authors.

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

package aggregatingcrdversiondiscovery

import (
	apiextensionsapiserverkcp "k8s.io/apiextensions-apiserver/pkg/kcp"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"

	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

type Config struct {
	Generic *genericapiserver.Config
	Extra   ExtraConfig
}

type ExtraConfig struct {
	DynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper

	CRDLister                  kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer
	APIBindingAwareCRDLister   apiextensionsapiserverkcp.ClusterAwareCRDClusterLister
	APIBindingInformer         apisv1alpha2informers.APIBindingClusterInformer
	APIBindingLister           apisv1alpha2listers.APIBindingClusterLister
	LocalAPIExportInformer     apisv1alpha2informers.APIExportClusterInformer
	GlobalAPIExportInformer    apisv1alpha2informers.APIExportClusterInformer
	GlobalShardClusterInformer corev1alpha1informers.ShardClusterInformer

	DynamicClusterClient kcpdynamic.ClusterInterface
	VWClientConfig       *rest.Config

	ShardName                      string
	ShardVirtualWorkspaceURLGetter func() string
}

type completedConfig struct {
	Generic genericapiserver.CompletedConfig
	Extra   *ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() CompletedConfig {
	if c == nil {
		return CompletedConfig{}
	}

	cfg := completedConfig{
		c.Generic.Complete(nil),
		&c.Extra,
	}
	// We do version discovery and nothing more.
	cfg.Generic.SkipOpenAPIInstallation = true

	return CompletedConfig{&cfg}
}

func (c *completedConfig) WithOpenAPIAggregationController(delegatedAPIServer *genericapiserver.GenericAPIServer) error {
	return nil
}

func NewConfig(
	cfg *genericapiserver.Config,

	dynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper,

	dynamicClusterClient kcpdynamic.ClusterInterface,
	vwClientConfig *rest.Config,
	shardName string,
	shardVirtualWorkspaceURLGetter func() string,

	crdLister kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingAwareCRDLister apiextensionsapiserverkcp.ClusterAwareCRDClusterLister,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	localAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
) (*Config, error) {
	rest.AddUserAgent(vwClientConfig, "kcp-aggregatingcrdversiondiscovery-apiserver")
	cfg.SkipOpenAPIInstallation = true

	ret := &Config{
		Generic: cfg,
		Extra: ExtraConfig{
			DynamicRESTMapper:              dynamicRESTMapper,
			DynamicClusterClient:           dynamicClusterClient,
			VWClientConfig:                 vwClientConfig,
			ShardName:                      shardName,
			ShardVirtualWorkspaceURLGetter: shardVirtualWorkspaceURLGetter,

			CRDLister:                  crdLister,
			APIBindingAwareCRDLister:   apiBindingAwareCRDLister,
			APIBindingInformer:         apiBindingInformer,
			LocalAPIExportInformer:     localAPIExportInformer,
			GlobalAPIExportInformer:    globalAPIExportInformer,
			GlobalShardClusterInformer: globalShardClusterInformer,
		},
	}

	return ret, nil
}
