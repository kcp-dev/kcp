/*
Copyright 2021 The KCP Authors.

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

package rootapiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/warning"
	"k8s.io/client-go/rest"
	componentbaseversion "k8s.io/component-base/version"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
)

type InformerStart func(stopCh <-chan struct{})
type InformerStarts []InformerStart

type RootAPIExtraConfig struct {
	// we phrase it like this so we can build the post-start-hook, but no one can take more indirect dependencies on informers
	informerStart func(stopCh <-chan struct{})

	VirtualWorkspaces []framework.VirtualWorkspace
}

// Validate helps ensure that we build this config correctly, because there are lots of bits to remember for now
func (c *RootAPIExtraConfig) Validate() error {
	ret := []error{}

	return utilerrors.NewAggregate(ret)
}

type RootAPIConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   RootAPIExtraConfig
}

// RootAPIServer is only responsible for serving the APIs for the virtual workspace
// at a given root path or root path family
// It does NOT expose oauth, related oauth endpoints, or any kube APIs.
type RootAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *RootAPIExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *RootAPIConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

func (c *completedConfig) WithOpenAPIAggregationController(delegatedAPIServer *genericapiserver.GenericAPIServer) error {
	return nil
}

func (c completedConfig) New(delegationTarget genericapiserver.DelegationTarget) (*RootAPIServer, error) {
	delegateAPIServer := delegationTarget

	var readys []framework.ReadyFunc
	vwNames := sets.NewString()
	for _, virtualWorkspace := range c.ExtraConfig.VirtualWorkspaces {
		name := virtualWorkspace.GetName()
		if vwNames.Has(name) {
			return nil, errors.New("Several virtual workspaces with the same name: " + name)
		}
		vwNames.Insert(name)

		var err error
		delegateAPIServer, err = virtualWorkspace.Register(c.GenericConfig, delegateAPIServer)
		if err != nil {
			return nil, err
		}
		readys = append(readys, virtualWorkspace.IsReady)
	}

	c.GenericConfig.BuildHandlerChainFunc = c.getRootHandlerChain(delegateAPIServer)
	c.GenericConfig.RequestInfoResolver = c
	c.GenericConfig.ReadyzChecks = append(c.GenericConfig.ReadyzChecks, asHealthCheck(readys))

	genericServer, err := c.GenericConfig.New("virtual-workspaces-root-apiserver", delegateAPIServer)
	if err != nil {
		return nil, err
	}

	s := &RootAPIServer{
		GenericAPIServer: genericServer,
	}

	// register our poststarthooks
	s.GenericAPIServer.AddPostStartHookOrDie("virtual-workspace-startinformers", func(context genericapiserver.PostStartHookContext) error {
		c.ExtraConfig.informerStart(context.StopCh)
		return nil
	})

	return s, nil
}

type asHealthCheck []framework.ReadyFunc

func (readys asHealthCheck) Name() string {
	return "VirtualWotrkspaceAdditionalReadinessChecks"
}

func (readys asHealthCheck) Check(req *http.Request) error {
	for _, ready := range readys {
		if err := ready(); err != nil {
			return err
		}
	}
	return nil
}

func (c completedConfig) resolveRootPaths(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	completedContext = requestContext
	for _, virtualWorkspace := range c.ExtraConfig.VirtualWorkspaces {
		if accepted, prefixToStrip, completedContext := virtualWorkspace.ResolveRootPath(urlPath, requestContext); accepted {
			return accepted, prefixToStrip, context.WithValue(completedContext, virtualcontext.VirtualWorkspaceNameKey, virtualWorkspace.GetName())
		}
	}
	return
}

func (c completedConfig) getRootHandlerChain(delegateAPIServer genericapiserver.DelegationTarget) func(http.Handler, *genericapiserver.Config) http.Handler {
	return func(apiHandler http.Handler, genericConfig *genericapiserver.Config) http.Handler {
		return genericapiserver.DefaultBuildHandlerChain(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// detect old kubectl plugins and inject warning headers
			if req.UserAgent() == "Go-http-client/2.0" {
				// TODO(sttts): in the future compare the plugin version to the server version and warn outside of skew compatibility guarantees.
				warning.AddWarning(req.Context(), "",
					fmt.Sprintf("You are using an old kubectl-kcp plugin. Please update to a version matching the kcp server version %q.", componentbaseversion.Get().GitVersion))
			}

			if accepted, prefixToStrip, context := c.resolveRootPaths(req.URL.Path, req.Context()); accepted {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixToStrip)
				req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefixToStrip)
				req = req.WithContext(context)
				delegatedHandler := delegateAPIServer.UnprotectedHandler()
				if delegatedHandler != nil {
					delegatedHandler.ServeHTTP(w, req)
				}
				return
			}
			apiHandler.ServeHTTP(w, req)
		}), c.GenericConfig.Config)
	}
}

var _ genericapirequest.RequestInfoResolver = (*completedConfig)(nil)

// NewRequestInfo method makes the `completedConfig` an implementation of a RequestInfoResolver.
// And when creating the RootAPIServer (in the New method), the RequestInfoResolver of the
// associated GenericConfig is replaced by the `completedConfig`.
//
// Since we reuse the DefaultBuildChainHandler in our customized getRootChainHandler method
// (that checks the URL Path against virtual workspace root paths), that means that the RequestInfo
// object will be created, as part of the DefaultBuildChainHandler.
//
// So we also override the RequestInfoResolver in order to use the same URL Path as the one
// that will be forwarded to the virtual workspace deletegated APIServers.
func (c completedConfig) NewRequestInfo(req *http.Request) (*genericapirequest.RequestInfo, error) {
	defaultResolver := genericapiserver.NewRequestInfoResolver(c.GenericConfig.Config)
	if accepted, prefixToStrip, _ := c.resolveRootPaths(req.URL.Path, req.Context()); accepted {
		p := strings.TrimPrefix(req.URL.Path, prefixToStrip)
		rp := strings.TrimPrefix(req.URL.RawPath, prefixToStrip)
		r2 := new(http.Request)
		*r2 = *req
		r2.URL = new(url.URL)
		*r2.URL = *req.URL
		r2.URL.Path = p
		r2.URL.RawPath = rp
		return defaultResolver.NewRequestInfo(r2)
	}
	return defaultResolver.NewRequestInfo(req)
}

func NewRootAPIConfig(recommendedConfig *genericapiserver.RecommendedConfig, informerStarts InformerStarts, virtualWorkspaces ...framework.VirtualWorkspace) (*RootAPIConfig, error) {
	// TODO: genericConfig.ExternalAddress = ... allow a command line flag or it to be overridden by a top-level multiroot apiServer

	// Loopback is not wired for now, since virtual workspaces are expected to delegate to
	// some APIServer.
	// The RootAPISrver is just a proxy to the various virtual workspaces.
	// We might consider a giving a special meaning to a global loopback config, in the future
	// but that's not the case for now.
	recommendedConfig.Config.LoopbackClientConfig = &rest.Config{
		Host: "loopback-config-not-wired-for-now",
	}

	// TODO: in the future it would probably be a mix between a delegated authorizer (delegating to some KCP instance)
	// and a specific authorizer whose rules would be defined by each prefix-based virtual workspace.
	recommendedConfig.Authorization.Authorizer = authorizerfactory.NewAlwaysAllowAuthorizer()

	ret := &RootAPIConfig{
		GenericConfig: recommendedConfig,
		ExtraConfig: RootAPIExtraConfig{
			informerStart: func(stopCh <-chan struct{}) {
				for _, informerStart := range informerStarts {
					informerStart(stopCh)
				}
			},
			VirtualWorkspaces: virtualWorkspaces,
		},
	}

	return ret, ret.ExtraConfig.Validate()
}
