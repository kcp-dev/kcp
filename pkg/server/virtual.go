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

package server

import (
	"net/http"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kaudit "k8s.io/apiserver/pkg/audit"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/client-go/rest"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	virtualoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

type mux interface {
	Handle(pattern string, handler http.Handler)
}

type VirtualConfig virtualrootapiserver.Config

type completedVirtualConfig struct {
	virtualrootapiserver.CompletedConfig
}

type CompletedVirtualConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedVirtualConfig
}

func newVirtualConfig(
	o kcpserveroptions.CompletedOptions,
	config *rest.Config,
	kubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory,
	kcpSharedInformerFactory, cacheKcpSharedInformerFactory kcpinformers.SharedInformerFactory,
	shardExternalURL func() string,
) (*VirtualConfig, error) {
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)

	recommendedConfig := genericapiserver.NewRecommendedConfig(codecs)
	// the recommended config attaches healthz.PingHealthz, healthz.LogHealthz
	// which already have been added to the server so just skip them here
	// otherwise we will panic with duplicate path registration of "/readyz/ping"
	recommendedConfig.HealthzChecks = []healthz.HealthChecker{}
	recommendedConfig.ReadyzChecks = []healthz.HealthChecker{}
	recommendedConfig.LivezChecks = []healthz.HealthChecker{}

	c, err := virtualrootapiserver.NewConfig(recommendedConfig)
	if err != nil {
		return nil, err
	}

	authorizationOptions := virtualoptions.NewAuthorization()
	authorizationOptions.AlwaysAllowGroups = o.Authorization.AlwaysAllowGroups
	authorizationOptions.AlwaysAllowPaths = o.Authorization.AlwaysAllowPaths
	if err := authorizationOptions.ApplyTo(&recommendedConfig.Config, func() []virtualrootapiserver.NamedVirtualWorkspace {
		return c.Extra.VirtualWorkspaces
	}); err != nil {
		return nil, err
	}

	c.Extra.VirtualWorkspaces, err = o.Virtual.VirtualWorkspaces.NewVirtualWorkspaces(
		config,
		virtualcommandoptions.DefaultRootPathPrefix,
		shardExternalURL,
		kubeSharedInformerFactory,
		kcpSharedInformerFactory,
		cacheKcpSharedInformerFactory,
	)
	if err != nil {
		return nil, err
	}

	return (*VirtualConfig)(c), nil
}

func (c *VirtualConfig) Complete(auth genericapiserver.AuthenticationInfo, auditEvaluator kaudit.PolicyRuleEvaluator, auditBackend kaudit.Backend, externalAddress string) CompletedVirtualConfig {
	if c == nil {
		return CompletedVirtualConfig{}
	}

	c.Generic.Authentication = auth
	c.Generic.ExternalAddress = externalAddress

	completed := &completedVirtualConfig{
		(*virtualrootapiserver.Config)(c).Complete(),
	}

	completed.Generic.AuditBackend = auditBackend
	completed.Generic.AuditPolicyRuleEvaluator = auditEvaluator

	return CompletedVirtualConfig{completed}
}

func (c CompletedVirtualConfig) NewServer(preHandlerChainMux mux) (*virtualrootapiserver.Server, error) {
	s, err := virtualrootapiserver.NewServer(c.CompletedConfig, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	preparedRootAPIServer := s.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := c.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return nil, err
	}

	preHandlerChainMux.Handle(virtualcommandoptions.DefaultRootPathPrefix+"/", preparedRootAPIServer.GenericAPIServer.Handler)

	return s, nil
}
