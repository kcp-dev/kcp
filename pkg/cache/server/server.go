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

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
)

type Server struct {
	CompletedConfig

	apiextensions *apiextensionsapiserver.CustomResourceDefinitions
}

func NewServer(c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}

	var err error
	s.apiextensions, err = s.ApiExtensions.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}
	return s, nil
}

// preparedGenericAPIServer is a private wrapper that enforces a call of PrepareRun() before Run can be invoked.
type preparedServer struct {
	*Server
	Handler *genericapiserver.APIServerHandler
}

func (s *Server) PrepareRun(ctx context.Context) (preparedServer, error) {
	logger := klog.FromContext(ctx).WithValues("component", "cache-server")
	if err := s.apiextensions.GenericAPIServer.AddPostStartHook("bootstrap-cache-server", func(hookContext genericapiserver.PostStartHookContext) error {
		logger := logger.WithValues("postStartHook", "bootstrap-cache-server")
		if err := bootstrap.Bootstrap(klog.NewContext(goContext(hookContext), logger), s.ApiExtensionsClusterClient); err != nil {
			logger.Error(err, "failed creating the static CustomResourcesDefinitions")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		return nil
	}); err != nil {
		return preparedServer{}, err
	}

	if err := s.apiextensions.GenericAPIServer.AddPostStartHook("cache-server-start-informers", func(hookContext genericapiserver.PostStartHookContext) error {
		logger := logger.WithValues("postStartHook", "cache-server-start-informers")
		s.ApiExtensionsSharedInformerFactory.Start(hookContext.StopCh)

		select {
		case <-hookContext.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting kube informers")
		return nil
	}); err != nil {
		return preparedServer{}, err
	}
	return preparedServer{s, s.apiextensions.GenericAPIServer.Handler}, nil
}

func (s preparedServer) Run(ctx context.Context) error {
	return s.apiextensions.GenericAPIServer.PrepareRun().Run(ctx.Done())
}

func (s preparedServer) RunPostStartHooks(stopCh <-chan struct{}) {
	s.apiextensions.GenericAPIServer.RunPostStartHooks(stopCh)
}

// goContext turns the PostStartHookContext into a context.Context for use in routines that may or may not
// run inside of a post-start-hook. The k8s APIServer wrote the post-start-hook context code before contexts
// were part of the Go stdlib.
func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}
