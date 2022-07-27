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

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/revdial"
)

func (s *Server) installTunneler(ctx context.Context, preHandlerChainMux mux) error {
	// create reverse tunnel pool
	pool := revdial.NewReversePool()
	s.AddPostStartHook("kcp-reverse-tunneler", func(hookContext genericapiserver.PostStartHookContext) error {
		go func() {
			<-hookContext.StopCh
			pool.Close()
		}()
		return nil
	})

	klog.Infof("Starting reverse proxy tunneler")
	preHandlerChainMux.Handle(virtualcommandoptions.DefaultRootPathPrefix+revdial.DefaultRootPathPrefix+"/", pool)

	return nil
}
