/*
Copyright 2025 The KCP Authors.

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

package virtualresources

import (
	"k8s.io/apimachinery/pkg/runtime"
	apiopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	openapicommon "k8s.io/kube-openapi/pkg/common"

	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
)

type Config struct {
	Generic *genericapiserver.Config
	Extra   ExtraConfig
}

type ExtraConfig struct {
	VWClientConfig       *rest.Config
	DynamicClusterClient kcpdynamic.ClusterInterface

	ShardVirtualWorkspaceURLGetter func() string

	CRDLister               kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer
	APIBindingInformer      apisv1alpha2informers.APIBindingClusterInformer
	LocalAPIExportInformer  apisv1alpha2informers.APIExportClusterInformer
	GlobalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer
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

	cfg.Generic.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		func(rc openapicommon.ReferenceCallback) map[string]openapicommon.OpenAPIDefinition {
			return map[string]openapicommon.OpenAPIDefinition{}
		},
		apiopenapi.NewDefinitionNamer(runtime.NewScheme()),
	)

	return CompletedConfig{&cfg}
}

func (c *completedConfig) WithOpenAPIAggregationController(delegatedAPIServer *genericapiserver.GenericAPIServer) error {
	return nil
}

func NewConfig(
	cfg *genericapiserver.Config,
	vwClientConfig *rest.Config,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	shardVirtualWorkspaceURLGetter func() string,
	crdLister kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	localAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) (*Config, error) {
	rest.AddUserAgent(vwClientConfig, "kcp-virtual-resources-apiserver")
	cfg.SkipOpenAPIInstallation = true

	ret := &Config{
		Generic: cfg,
		Extra: ExtraConfig{
			VWClientConfig:       vwClientConfig,
			DynamicClusterClient: dynamicClusterClient,

			ShardVirtualWorkspaceURLGetter: shardVirtualWorkspaceURLGetter,

			CRDLister:               crdLister,
			APIBindingInformer:      apiBindingInformer,
			LocalAPIExportInformer:  localAPIExportInformer,
			GlobalAPIExportInformer: globalAPIExportInformer,
		},
	}

	return ret, nil
}
