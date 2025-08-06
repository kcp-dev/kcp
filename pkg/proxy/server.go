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
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/authentication"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	frontproxyfilters "github.com/kcp-dev/kcp/pkg/proxy/filters"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
	"github.com/kcp-dev/kcp/pkg/proxy/metrics"
	kcpfilters "github.com/kcp-dev/kcp/pkg/server/filters"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"

	_ "k8s.io/component-base/metrics/prometheus/workqueue"
)

type Server struct {
	CompletedConfig
	Handler                  http.Handler
	IndexController          *index.Controller
	AuthController           *authentication.Controller
	KcpSharedInformerFactory kcpinformers.SharedScopedInformerFactory
}

func NewServer(ctx context.Context, c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}

	// load the configured path mappings
	mappings, err := loadMappings(c.Options.MappingFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load mappings from %q: %w", c.Options.MappingFile, err)
	}
	hasShardMapping := slices.ContainsFunc(mappings, isShardMapping)

	// When a mapping for shards is configured, we need to build up an index of which shard hosts
	// which workspaces and logicalclusters. Ideally we'd start the index controller only if there
	// is a shard mapping (i.e. a "/clusters/" entry in the mapping list), but there are other places
	// in the general server package that always lookup the shard, so making the index optional would
	// require deeper refactoring to make everything rely on the lookup middleware.
	// If so, there will be also another middleware injected into the handler chain, which is responsible
	// for resolving the incoming request and store the found cluster name in a context variable.
	rootShardConfigInformerClient, err := kcpclientset.NewForConfig(s.CompletedConfig.RootShardConfig)
	if err != nil {
		return s, fmt.Errorf("failed to create client for informers: %w", err)
	}
	s.KcpSharedInformerFactory = kcpinformers.NewSharedScopedInformerFactoryWithOptions(rootShardConfigInformerClient.Cluster(core.RootCluster.Path()), 30*time.Minute)

	getClientFunc := func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error) {
		shardConfig := restclient.CopyConfig(s.CompletedConfig.ShardsConfig)
		shardConfig.Host = shard.Spec.BaseURL
		shardClient, err := kcpclientset.NewForConfig(shardConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create shard %q client: %w", shard.Name, err)
		}
		return shardClient, nil
	}

	// This controller is responsible for watching all Shards, connecting to each of them and
	// watching a number of resources on each. The controller is then also satisfying the Index
	// interface.
	s.IndexController = index.NewController(ctx, s.KcpSharedInformerFactory.Core().V1alpha1().Shards(), getClientFunc)

	handler, err := NewHandler(ctx, mappings, s.IndexController)
	if err != nil {
		return s, err
	}

	// The optional auth handler will call the underlying authenticator only if
	// auth methods are configured directly on the front-proxy *or* if there is
	// a custom workspace authenticator, i.e. the AdditionalAuthEnabled field
	// only represents the CLI flag state.
	failedHandler := frontproxyfilters.NewUnauthorizedHandler()
	handler = frontproxyfilters.WithOptionalAuthentication(
		handler,
		failedHandler,
		s.completedConfig.AuthenticationInfo.Authenticator,
		s.CompletedConfig.AdditionalAuthEnabled)

	// Make the per-workspace authenticator available to the previous middleware
	// by hooking up a handler and a runtime index.
	hasWorkspaceAuth := hasShardMapping && kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.WorkspaceAuthentication)

	if hasWorkspaceAuth {
		// This controller is similar to the index controller, but keeps track of the per-workspace authenticators.
		s.AuthController = authentication.NewController(ctx, s.KcpSharedInformerFactory.Core().V1alpha1().Shards(), getClientFunc, nil)

		// When workspace auth is enabled, it depends on the target cluster whether
		// a custom authenticator exists or not. This needs to be determined before
		// the optionalAuthentication middleware can run, as it needs to know about
		// the workspace authenticator.
		handler = authentication.WithWorkspaceAuthResolver(handler, s.AuthController)
	}

	if hasShardMapping {
		// This middleware must happen before the authentication.
		handler = lookup.WithClusterResolver(handler, s.IndexController)
	}

	requestInfoFactory := requestinfo.NewFactory()
	handler = kcpfilters.WithInClusterServiceAccountRequestRewrite(handler)
	handler = genericapifilters.WithRequestInfo(handler, requestInfoFactory)
	handler = genericfilters.WithHTTPLogging(handler)
	handler = metrics.WithLatencyTracking(handler)
	handler = genericfilters.WithPanicRecovery(handler, requestInfoFactory)
	handler = genericfilters.WithCORS(handler, c.Options.CorsAllowedOriginList, nil, nil, nil, "true")

	mux := http.NewServeMux()
	// TODO: implement proper readyz handler
	mux.Handle("/readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
	}))

	// TODO: implement proper livez handler
	mux.Handle("/livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
	}))

	mux.Handle("/", handler)
	s.Handler = mux

	return s, nil
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

	if err := wait.PollUntilContextCancel(ctx, time.Millisecond*500, true, func(ctx context.Context) (bool, error) {
		if err := s.CompletedConfig.ResolveIdentities(ctx); err != nil {
			logger.V(3).Info("failed to resolve identities, keeping trying", "err", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to get or create identities: %w", err)
	}

	// start indexes
	go s.IndexController.Start(ctx, 2)

	if s.AuthController != nil {
		go s.AuthController.Start(ctx, 2)
	}

	s.KcpSharedInformerFactory.Start(ctx.Done())
	s.KcpSharedInformerFactory.WaitForCacheSync(ctx.Done())

	doneCh, _, err := s.CompletedConfig.ServingInfo.Serve(s.Handler, time.Second*60, ctx.Done())
	if err != nil {
		return err
	}

	<-doneCh
	return nil
}
