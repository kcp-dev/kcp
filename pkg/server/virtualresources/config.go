package virtualresources

import (
	"k8s.io/apimachinery/pkg/runtime"
	apiopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	openapicommon "k8s.io/kube-openapi/pkg/common"
)

type Config struct {
	Generic *genericapiserver.Config
	Extra   ExtraConfig
}

type ExtraConfig struct {
	VWClientConfig *rest.Config
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

func NewConfig(cfg *genericapiserver.Config, vwClientConfig *rest.Config) (*Config, error) {
	rest.AddUserAgent(vwClientConfig, ControllerName)
	cfg.SkipOpenAPIInstallation = true

	ret := &Config{
		Generic: cfg,
		Extra: ExtraConfig{
			VWClientConfig: vwClientConfig,
		},
	}

	return ret, nil
}
