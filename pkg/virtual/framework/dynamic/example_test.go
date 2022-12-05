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

	"github.com/kcp-dev/logicalcluster/v3"

	genericapiserver "k8s.io/apiserver/pkg/server"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	virtualframework "github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

// Typical Usage of a DynamicVirtualWorkspace
func Example() {

	var someAPIDefinitionSetGetter apidefinition.APIDefinitionSetGetter
	readyCh := make(chan struct{})

	var _ = dynamic.DynamicVirtualWorkspace{
		RootPathResolver: virtualframework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			if someAPIDefinitionSetGetter == nil {
				// If the APIDefinitionSetGetter is not initialized, don't accept the request
				return
			}

			var apiDomainKey dynamiccontext.APIDomainKey

			// Resolve the request root path and extract the API domain key from it

			// If the root path doesn't start by the right prefix or doesn't contain the API domain key,
			// just don't accept the request and return.

			// Add the apiDomainKey to the request context before passing the request to the virtual workspace APIServer
			completedContext = dynamiccontext.WithAPIDomainKey(requestContext, apiDomainKey)
			accepted = true
			return
		}),

		ReadyChecker: virtualframework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("syncer virtual workspace controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {

			// Initialize the implementation of the APIDefinitionSetGetter

			someAPIDefinitionSetGetter = newAPIDefinitionSetGetter()

			// Setup some controller that will add APIDefinitions on demand

			someController := setupController(func(logicalClusterName logicalcluster.Name, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string) (apidefinition.APIDefinition, error) {
				// apiserver.CreateServingInfoFor() creates and initializes all the required information to serve an API
				return apiserver.CreateServingInfoFor(mainConfig, apiResourceSchema, version, someRestProviderFunc)
			})

			// Start the controllers in a PostStartHook

			if err := mainConfig.AddPostStartHook("SomeDynamicVirtualWorkspacePostStartHook", func(hookContext genericapiserver.PostStartHookContext) error {

				// Wait for required informers to be synced

				someController.Start()

				close(readyCh)
				return nil
			}); err != nil {
				return nil, err
			}

			return someAPIDefinitionSetGetter, nil
		},
	}
}

func newAPIDefinitionSetGetter() apidefinition.APIDefinitionSetGetter { return nil }

type someController interface {
	Start()
}

// CreateAPIDefinitionFunc is the type of a function which allows creating an APIDefinition
// (with REST storage and handler Request scopes) based on the API specification logical cluster name and OpenAPI v3 schema.
type CreateAPIDefinitionFunc func(logicalClusterName logicalcluster.Name, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string) (apidefinition.APIDefinition, error)

func setupController(createAPIDefinition CreateAPIDefinitionFunc) someController {
	return nil
}

var someRestProviderFunc apiserver.RestProviderFunc
