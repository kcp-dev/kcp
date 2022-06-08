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
	"errors"
	"fmt"
	"net/http"
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
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
	}

	c.GenericConfig.BuildHandlerChainFunc = c.getRootHandlerChain(delegateAPIServer)
	c.GenericConfig.ReadyzChecks = append(c.GenericConfig.ReadyzChecks, asHealthChecks(c.ExtraConfig.VirtualWorkspaces)...)

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

type asHealthCheck struct {
	framework.VirtualWorkspace
}

func (vw asHealthCheck) Name() string {
	return vw.GetName()
}

func (vw asHealthCheck) Check(req *http.Request) error {
	return vw.IsReady()
}

func asHealthChecks(workspaces []framework.VirtualWorkspace) []healthz.HealthChecker {
	var healthCheckers []healthz.HealthChecker
	for _, vw := range workspaces {
		healthCheckers = append(healthCheckers, asHealthCheck{vw})
	}
	return healthCheckers
}

func (c completedConfig) getRootHandlerChain(delegateAPIServer genericapiserver.DelegationTarget) func(http.Handler, *genericapiserver.Config) http.Handler {
	return func(apiHandler http.Handler, genericConfig *genericapiserver.Config) http.Handler {
		delegateAfterDefaultHandlerChain := genericapiserver.DefaultBuildHandlerChain(
			http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if _, virtualWorkspaceNameExists := virtualcontext.VirtualWorkspaceNameFrom(req.Context()); virtualWorkspaceNameExists {
					delegatedHandler := delegateAPIServer.UnprotectedHandler()
					if delegatedHandler != nil {
						delegatedHandler.ServeHTTP(w, req)
					}
					return
				}
				apiHandler.ServeHTTP(w, req)
			}), c.GenericConfig.Config)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requestContext := req.Context()
			// detect old kubectl plugins and inject warning headers
			if req.UserAgent() == "Go-http-client/2.0" {
				// TODO(sttts): in the future compare the plugin version to the server version and warn outside of skew compatibility guarantees.
				warning.AddWarning(requestContext, "",
					fmt.Sprintf("You are using an old kubectl-kcp plugin. Please update to a version matching the kcp server version %q.", componentbaseversion.Get().GitVersion))
			}

			for _, virtualWorkspace := range c.ExtraConfig.VirtualWorkspaces {
				if accepted, prefixToStrip, completedContext := virtualWorkspace.ResolveRootPath(req.URL.Path, requestContext); accepted {
					req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixToStrip)
					req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefixToStrip)
					req = req.WithContext(virtualcontext.WithVirtualWorkspaceName(completedContext, virtualWorkspace.GetName()))
					break
				}
			}
			delegateAfterDefaultHandlerChain.ServeHTTP(w, req)
		})
	}
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
