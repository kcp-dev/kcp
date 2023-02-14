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
	"context"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kaudit "k8s.io/apiserver/pkg/audit"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	virtualoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
)

type mux interface {
	Handle(pattern string, handler http.Handler)
}

func (s *Server) installVirtualWorkspaces(
	ctx context.Context,
	config *rest.Config,
	server *genericapiserver.GenericAPIServer,
	auth genericapiserver.AuthenticationInfo,
	externalAddress string,
	auditEvaluator kaudit.PolicyRuleEvaluator,
	preHandlerChainMux mux,
) error {
	logger := klog.FromContext(ctx)
	// create virtual workspaces
	virtualWorkspaces, err := s.Options.Virtual.VirtualWorkspaces.NewVirtualWorkspaces(
		config,
		virtualcommandoptions.DefaultRootPathPrefix,
		s.KubeSharedInformerFactory,
		s.KcpSharedInformerFactory,
		s.CacheKcpSharedInformerFactory,
	)
	if err != nil {
		return err
	}

	// create apiserver, with its own delegation chain
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
	recommendedConfig.Authentication = auth

	authorizationOptions := virtualoptions.NewAuthorization()
	authorizationOptions.AlwaysAllowGroups = s.Options.Authorization.AlwaysAllowGroups
	authorizationOptions.AlwaysAllowPaths = s.Options.Authorization.AlwaysAllowPaths
	if err := authorizationOptions.ApplyTo(&recommendedConfig.Config, virtualWorkspaces); err != nil {
		return err
	}

	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(recommendedConfig, nil, virtualWorkspaces)
	if err != nil {
		return err
	}
	rootAPIServerConfig.GenericConfig.ExternalAddress = externalAddress

	completedRootAPIServerConfig := rootAPIServerConfig.Complete()
	completedRootAPIServerConfig.GenericConfig.AuditBackend = server.AuditBackend
	completedRootAPIServerConfig.GenericConfig.AuditPolicyRuleEvaluator = auditEvaluator

	rootAPIServer, err := completedRootAPIServerConfig.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}

	if err := server.AddReadyzChecks(completedRootAPIServerConfig.GenericConfig.ReadyzChecks...); err != nil {
		return err
	}

	preparedRootAPIServer := rootAPIServer.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := completedRootAPIServerConfig.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return err
	}

	if err := s.AddPostStartHook("kcp-start-virtual-workspace", func(ctx genericapiserver.PostStartHookContext) error {
		preparedRootAPIServer.RunPostStartHooks(ctx.StopCh)
		return nil
	}); err != nil {
		return err
	}

	logger.Info("starting virtual workspace apiserver")
	preHandlerChainMux.Handle(virtualcommandoptions.DefaultRootPathPrefix+"/", preparedRootAPIServer.GenericAPIServer.Handler)

	return nil
}
