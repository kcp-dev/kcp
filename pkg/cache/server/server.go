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

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/util"
)

type Server struct {
	CompletedConfig
}

func NewServer(c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}
	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("component", "cache-server")
	server, err := s.ApiExtensions.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}
	if err := server.GenericAPIServer.AddPostStartHook("bootstrap-cache-server", func(hookContext genericapiserver.PostStartHookContext) error {
		logger = logger.WithValues("postStartHook", "bootstrap-cache-server")
		if err = bootstrap.Bootstrap(klog.NewContext(util.GoContext(hookContext), logger), s.ApiExtensionsClusterClient); err != nil {
			logger.Error(err, "failed creating the static CustomResourcesDefinitions")
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		return nil
	}); err != nil {
		return err
	}

	if err := server.GenericAPIServer.AddPostStartHook("cache-server-start-informers", func(hookContext genericapiserver.PostStartHookContext) error {
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
		return err
	}
	return server.GenericAPIServer.PrepareRun().Run(ctx.Done())
}
