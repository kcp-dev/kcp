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
	"k8s.io/client-go/rest"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	cacheserver "github.com/kcp-dev/kcp/pkg/cache/server"
)

// installCacheServer integrates the cache-server into the kcp-server.
// The new server is available at /services/cache/... HTTP path.
func (s *Server) installCacheServer(ctx context.Context) error {
	// a hack for preventing overwriting of the client/server certs
	// when we run in embedded etcd server
	// this could be reworked if we were providing Config/CompletedConfig for the additional servers
	wasEmbeddedEtcdEnabled := s.Options.EmbeddedEtcd.Enabled
	s.Options.Cache.Server.EmbeddedEtcd.Enabled = false
	newCacheServerConfig, err := cacheserver.NewConfig(s.Options.Cache.Server, rest.CopyConfig(s.GenericConfig.LoopbackClientConfig))
	if err != nil {
		return err
	}
	s.Options.Cache.Server.EmbeddedEtcd.Enabled = wasEmbeddedEtcdEnabled
	completedCacheServerConfig, err := newCacheServerConfig.Complete()
	if err != nil {
		return err
	}
	completedCacheServerConfig.EmbeddedEtcd = s.EmbeddedEtcd
	newCacheServer, err := cacheserver.NewServer(completedCacheServerConfig)
	if err != nil {
		return err
	}
	preparedCacheServer, err := newCacheServer.PrepareRun(ctx)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook("kcp-start-cache-server", func(ctx genericapiserver.PostStartHookContext) error {
		preparedCacheServer.RunPostStartHooks(ctx.StopCh)
		return nil
	}); err != nil {
		return err
	}

	s.preHandlerChainMux.Handle(virtualcommandoptions.DefaultRootPathPrefix+"/cache/", preparedCacheServer.Handler)
	return nil
}
