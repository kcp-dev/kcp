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
	"net/url"
	"path"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

type mux interface {
	Handle(pattern string, handler http.Handler)
}

func (s *Server) installVirtualWorkspaces(ctx context.Context, kubeClusterClient kubernetesclient.ClusterInterface, dynamicClusterClient dynamic.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, auth genericapiserver.AuthenticationInfo, externalAddress string, preHandlerChainMux mux) error {
	// create virtual workspaces
	extraInformerStarts, virtualWorkspaces, err := s.options.Virtual.VirtualWorkspaces.NewVirtualWorkspaces(
		virtualcommandoptions.DefaultRootPathPrefix,
		kubeClusterClient,
		dynamicClusterClient,
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
	rootAPIServerConfig.GenericConfig.ExternalAddress = externalAddress
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
	preHandlerChainMux.Handle(virtualcommandoptions.DefaultRootPathPrefix+"/", preparedRootAPIServer.GenericAPIServer.Handler)

	return nil
}

func (s *Server) installVirtualWorkspacesRedirect(ctx context.Context, preHandlerChainMux mux) error {
	// TODO(sttts): protect redirect via authz?

	externalBaseURL, err := url.Parse(s.options.Virtual.ExternalVirtualWorkspaceAddress)
	if err != nil {
		return err // shouldn't happen due to options validation
	}

	from := virtualcommandoptions.DefaultRootPathPrefix + "/"
	to := *externalBaseURL // shallow copy
	to.Path = path.Join(to.Path, virtualcommandoptions.DefaultRootPathPrefix, "/")
	klog.Infof("Redirecting %s to %s", from, to.String())

	preHandlerChainMux.Handle(from, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := *externalBaseURL // shallow copy
		u.Path = path.Join(u.Path, r.URL.Path)

		klog.Infof("Got virtual workspace request to %s, redirecting to %s", r.URL.Path, u.String())

		http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
	}))

	return nil
}
