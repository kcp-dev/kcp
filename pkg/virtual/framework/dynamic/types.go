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

package dynamic

import (
	"context"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
)

var _ framework.VirtualWorkspace = (*DynamicVirtualWorkspace)(nil)

// DynamicVirtualWorkspace is an implementation of a framework.VirtualWorkspace which can dynamically serve resources,
// based on API definitions (including an OpenAPI v3 schema), and a Rest storage provider.
type DynamicVirtualWorkspace struct {
	Name             framework.VirtualWorkspaceName
	RootPathResolver framework.RootPathResolverFunc
	Authorizer       authorizer.AuthorizerFunc
	Ready            framework.ReadyFunc

	// BootstrapAPISetManagement creates, initializes and returns an apidefinition.APIDefinitionSetGetter.
	// Usually it would also set up some logic that will call the apiserver.CreateServingInfoFor() method
	// to add an apidefinition.APIDefinition in the apidefinition.APIDefinitionSetGetter on some event.
	BootstrapAPISetManagement func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error)
}

func (vw *DynamicVirtualWorkspace) Names() framework.VirtualWorkspaceNames {
	return []framework.VirtualWorkspaceName{vw.Name}
}

func (vw *DynamicVirtualWorkspace) HealthCheckers() []healthz.HealthChecker {
	return []healthz.HealthChecker{vw.Ready.HealthCheck(vw.Name)}
}

func (vw *DynamicVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, name framework.VirtualWorkspaceName, completedContext context.Context) {
	accepted, prefixToStrip, completedContext = vw.RootPathResolver(urlPath, context)
	name = vw.Name
	return
}

func (vw *DynamicVirtualWorkspace) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if vw.Authorizer != nil {
		return vw.Authorizer(ctx, a)
	}
	return authorizer.DecisionNoOpinion, "", nil
}
