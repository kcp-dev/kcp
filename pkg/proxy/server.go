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

package proxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/util/wait"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	coreinformers "github.com/kcp-dev/client-go/informers"
	coreclient "github.com/kcp-dev/client-go/kubernetes"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	frontproxyfilters "github.com/kcp-dev/kcp/pkg/proxy/filters"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/proxy/usergating"
	"github.com/kcp-dev/kcp/pkg/proxy/usergating/ruleset"
	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

type Server struct {
	CompletedConfig
	Handler                   http.Handler
	IndexController           *index.Controller
	UserGateController        *usergating.Controller
	KcpSharedInformerFactory  kcpinformers.SharedInformerFactory
	CoreSharedInformerFactory coreinformers.SharedInformerFactory
}

func NewServer(ctx context.Context, c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}

	rootShardConfigInformerConfig := kcpclienthelper.SetCluster(restclient.CopyConfig(s.CompletedConfig.RootShardConfig), tenancyv1alpha1.RootCluster)
	rootShardConfigInformerClient, err := kcpclient.NewForConfig(rootShardConfigInformerConfig)
	if err != nil {
		return s, fmt.Errorf("failed to create client for informers: %w", err)
	}
	s.KcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(rootShardConfigInformerClient, 30*time.Minute)
	s.IndexController = index.NewController(
		ctx,
		s.CompletedConfig.RootShardConfig.Host,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclient.Interface, error) {
			shardConfig := restclient.CopyConfig(s.CompletedConfig.RootShardConfig)
			shardConfig.Host = shard.Spec.BaseURL
			shardClient, err := kcpclient.NewForConfig(kcpclienthelper.SetCluster(restclient.CopyConfig(shardConfig), logicalcluster.Wildcard))
			if err != nil {
				return nil, fmt.Errorf("failed to create shard %q client: %w", shard.Name, err)
			}
			return shardClient, nil
		},
	)

	if c.Options.UserGating.Enabled {
		// setup the shared informer factory
		rootShardCoreInformerClient, err := coreclient.NewForConfig(s.CompletedConfig.RootShardConfig)
		if err != nil {
			return s, fmt.Errorf("failed to create client for informers: %w", err)
		}
		s.CoreSharedInformerFactory = coreinformers.NewSharedInformerFactoryWithOptions(rootShardCoreInformerClient, 30*time.Minute)

		// load the default rule set from file passed in options
		data, err := os.ReadFile(c.Options.UserGating.DefaultRulesFile)
		if err != nil {
			return nil, err
		}
		rules, err := ruleset.FromYAML(data)
		if err != nil {
			return nil, err
		}

		// create the controller to watch for updates to the usergating secret in root:kcp-system
		s.UserGateController = usergating.NewController(ctx, s.CoreSharedInformerFactory, rules)
	}

	s.Handler, err = NewHandler(ctx, s.CompletedConfig.Options, s.IndexController)
	return s, err
}

// preparedServer is a private wrapper that enforces a call of PrepareRun() before Run can be invoked.
type preparedServer struct {
	*Server
}

func (s *Server) PrepareRun(ctx context.Context) (preparedServer, error) {
	return preparedServer{s}, nil
}

func (s preparedServer) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("component", "proxy")

	if err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond*500, func(ctx context.Context) (bool, error) {
		if err := s.CompletedConfig.ResolveIdentities(ctx); err != nil {
			logger.V(3).Info("failed to resolve identities, keeping trying", "err", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get or create identities: %w", err)
	}

	// start the index controller
	go s.IndexController.Start(ctx, 2)
	s.KcpSharedInformerFactory.Start(ctx.Done())
	s.KcpSharedInformerFactory.WaitForCacheSync(ctx.Done())

	// setup the handler chain
	unauthorizedHandler := frontproxyfilters.NewUnauthorizedHandler()

	// Start the user gate controller if it's enabled. This is near the end of
	// the handler chain since it's really an authz check without the k8s
	// machinery, and we need authn to populate user info.
	if s.CompletedConfig.Options.UserGating.Enabled {
		go s.UserGateController.Start(ctx, 2)
		s.CoreSharedInformerFactory.Start(ctx.Done())
		s.CoreSharedInformerFactory.WaitForCacheSync(ctx.Done())
		s.Handler = usergating.WithUserGating(s.Handler, unauthorizedHandler, s.UserGateController.GetRuleSet)
	}

	s.Handler = frontproxyfilters.WithOptionalAuthentication(
		s.Handler,
		unauthorizedHandler,
		s.CompletedConfig.AuthenticationInfo.Authenticator,
		s.CompletedConfig.AdditionalAuthEnabled)

	requestInfoFactory := requestinfo.NewFactory()
	s.Handler = server.WithInClusterServiceAccountRequestRewrite(s.Handler)
	s.Handler = genericapifilters.WithRequestInfo(s.Handler, requestInfoFactory)
	s.Handler = genericfilters.WithHTTPLogging(s.Handler)
	s.Handler = genericfilters.WithPanicRecovery(s.Handler, requestInfoFactory)

	// start the server
	doneCh, _, err := s.CompletedConfig.ServingInfo.Serve(s.Handler, time.Second*60, ctx.Done())
	if err != nil {
		return err
	}

	<-doneCh
	return nil
}
