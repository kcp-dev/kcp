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
	"fmt"
	"net/http"
	"net/url"
	"path"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

type mux interface {
	Handle(pattern string, handler http.Handler)
}

func (s *Server) installVirtualWorkspaces(ctx context.Context, kubeClusterClient kubernetesclient.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, auth genericapiserver.AuthenticationInfo, preHandlerChainMux mux) error {
	// create virtual workspaces
	extraInformerStarts, virtualWorkspaces, err := s.options.Virtual.Workspaces.NewVirtualWorkspaces(
		kubeClusterClient,
		kcpClusterClient,
		s.kubeSharedInformerFactory,
		s.kcpSharedInformerFactory,
	)
	if err != nil {
		return err
	}
	s.AddPostStartHook("kcp-start-virtual-workspace-extra-informers", func(ctx genericapiserver.PostStartHookContext) error {
		for _, start := range extraInformerStarts {
			start(ctx.StopCh)
		}

		// TODO(sttts): wait for cache sync

		return nil
	})

	// create apiserver, with its own delegation chain
	scheme := runtime.NewScheme()
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	codecs := serializer.NewCodecFactory(scheme)
	recommendedConfig := genericapiserver.NewRecommendedConfig(codecs)
	recommendedConfig.Authentication = auth
	rootAPIServerConfig, err := virtualrootapiserver.NewRootAPIConfig(recommendedConfig, extraInformerStarts, virtualWorkspaces...)
	if err != nil {
		return err
	}
	rootAPIServerConfig.GenericConfig.ExternalAddress = fmt.Sprintf("%s:%d", s.options.GenericControlPlane.GenericServerRunOptions.ExternalHost, s.options.GenericControlPlane.SecureServing.BindPort)
	completedRootAPIServerConfig := rootAPIServerConfig.Complete()
	rootAPIServer, err := completedRootAPIServerConfig.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}
	preparedRootAPIServer := rootAPIServer.GenericAPIServer.PrepareRun()

	// this **must** be done after PrepareRun() as it sets up the openapi endpoints
	if err := completedRootAPIServerConfig.WithOpenAPIAggregationController(preparedRootAPIServer.GenericAPIServer); err != nil {
		return err
	}

	s.AddPostStartHook("kcp-start-virtual-workspace", func(ctx genericapiserver.PostStartHookContext) error {
		preparedRootAPIServer.RunPostStartHooks(ctx.StopCh)
		return nil
	})

	klog.Infof("Starting virtual workspace apiserver")
	preHandlerChainMux.Handle(s.options.Virtual.Workspaces.RootPathPrefix+"/", preparedRootAPIServer.GenericAPIServer.Handler)

	return nil
}
