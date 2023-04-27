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
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	genericfilters "k8s.io/apiserver/pkg/server/filters"
	restclient "k8s.io/client-go/rest"
	_ "k8s.io/component-base/metrics/prometheus/workqueue"
	"k8s.io/klog/v2"

	frontproxyfilters "github.com/kcp-dev/kcp/pkg/proxy/filters"
	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/proxy/metrics"
	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

type Server struct {
	CompletedConfig
	Handler                  http.Handler
	IndexController          *index.Controller
	KcpSharedInformerFactory kcpinformers.SharedScopedInformerFactory
}

func NewServer(ctx context.Context, c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}
	rootShardConfigInformerClient, err := kcpclientset.NewForConfig(s.CompletedConfig.RootShardConfig)
	if err != nil {
		return s, fmt.Errorf("failed to create client for informers: %w", err)
	}
	s.KcpSharedInformerFactory = kcpinformers.NewSharedScopedInformerFactoryWithOptions(rootShardConfigInformerClient.Cluster(core.RootCluster.Path()), 30*time.Minute)
	s.IndexController = index.NewController(
		ctx,
		s.KcpSharedInformerFactory.Core().V1alpha1().Shards(),
		func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error) {
			shardConfig := restclient.CopyConfig(s.CompletedConfig.ShardsConfig)
			shardConfig.Host = shard.Spec.BaseURL
			shardClient, err := kcpclientset.NewForConfig(shardConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to create shard %q client: %w", shard.Name, err)
			}
			return shardClient, nil
		},
	)

	handler, err := NewHandler(ctx, s.CompletedConfig.Options, s.IndexController)
	if err != nil {
		return s, err
	}

	failedHandler := frontproxyfilters.NewUnauthorizedHandler()
	handler = frontproxyfilters.WithOptionalAuthentication(
		handler,
		failedHandler,
		s.CompletedConfig.AuthenticationInfo.Authenticator,
		s.CompletedConfig.AdditionalAuthEnabled)

	requestInfoFactory := requestinfo.NewFactory()
	handler = server.WithInClusterServiceAccountRequestRewrite(handler)
	handler = genericapifilters.WithRequestInfo(handler, requestInfoFactory)
	handler = genericfilters.WithHTTPLogging(handler)
	handler = metrics.WithLatencyTracking(handler)
	handler = genericfilters.WithPanicRecovery(handler, requestInfoFactory)

	mux := http.NewServeMux()
	// TODO: implement proper readyz handler
	mux.Handle("/readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
	}))

	// TODO: implement proper livez handler
	mux.Handle("/livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK")) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
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

const workerCount = 10

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

	// start index
	go s.IndexController.Start(ctx, workerCount)

	s.KcpSharedInformerFactory.Start(ctx.Done())
	s.KcpSharedInformerFactory.WaitForCacheSync(ctx.Done())

	doneCh, _, err := s.CompletedConfig.ServingInfo.Serve(s.Handler, time.Second*60, ctx.Done())
	if err != nil {
		return err
	}

	<-doneCh
	return nil
}
