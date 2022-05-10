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

package dynamic_test

import (
	"context"
	"errors"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
)

// Typical Usage of a DynamicVirtualWorkspace
func Example() {

	var someAPISetRetriever apidefs.APIDefinitionSetGetter
	ready := false

	var _ = dynamic.DynamicVirtualWorkspace{

		Name: "SomeDynamicVirtualWorkspace",

		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			if someAPISetRetriever == nil {
				// If the APISetRetriever is not initialized, don't accept the request
				return
			}

			var apiDomainKey string

			// Resolve the request root path, and extract API domain key from it ...

			if _, exists := someAPISetRetriever.GetAPIDefinitionSet(apiDomainKey); !exists {
				// If the APISetRetriever doesn't manage this api domain, don't accept the request
				return
			}

			// ...

			// Add the apiDomainKey to the request context before passing the request to the virtual workspace APIServer
			completedContext = context.WithValue(requestContext, apidefs.APIDomainKeyContextKey, apiDomainKey)
			accepted = true
			return
		},

		Ready: func() error {
			if !ready {
				return errors.New("virtual workspace controllers are not started")
			}
			return nil
		},

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefs.APIDefinitionSetGetter, error) {

			// Initialize the implementation of the APISetRetriever

			someAPISetRetriever = newAPISetRetriever()

			// Setup some controller that will add APIDefinitions on demand

			someController := setupController(func(logicalClusterName string, spec *v1alpha1.CommonAPIResourceSpec) (apidefs.APIDefinition, error) {
				// apiserver.CreateServingInfoFor() creates and initializes all the required information to serve an API
				return apiserver.CreateServingInfoFor(mainConfig, logicalClusterName, spec, someRestProviderFunc)
			})

			// Start the controllers in a PostStartHook

			if err := mainConfig.AddPostStartHook("SomeDynamicVirtualWorkspacePostStartHook", func(hookContext genericapiserver.PostStartHookContext) error {

				// Wait for required informers to be synced

				someController.Start()

				ready = true
				return nil
			}); err != nil {
				return nil, err
			}

			return someAPISetRetriever, nil
		},
	}
}

func newAPISetRetriever() apidefs.APIDefinitionSetGetter { return nil }

type someController interface {
	Start()
}

func setupController(createAPIDefinition apidefs.CreateAPIDefinitionFunc) someController { return nil }

var someRestProviderFunc apiserver.RestProviderFunc
