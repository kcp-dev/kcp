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

package handlerbased

import (
	"context"
	"net/http"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/server/healthz"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

var _ framework.VirtualWorkspace = (*HandlerBasedVirtualWorkspace)(nil)

type HandlerBasedVirtualWorkspace struct {
	Name             framework.VirtualWorkspaceName
	RootPathResolver framework.RootPathResolverFunc
	Authorizer       authorizer.AuthorizerFunc
	Ready            framework.ReadyFunc
	BootstrapHandler func(mainConfig genericapiserver.CompletedConfig) (http.Handler, error)
}

func (vw *HandlerBasedVirtualWorkspace) Names() framework.VirtualWorkspaceNames {
	return []framework.VirtualWorkspaceName{vw.Name}
}

func (vw *HandlerBasedVirtualWorkspace) HealthCheckers() []healthz.HealthChecker {
	return []healthz.HealthChecker{vw.Ready.HealthCheck(vw.Name)}
}

func (vw *HandlerBasedVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, name framework.VirtualWorkspaceName, completedContext context.Context) {
	accepted, prefixToStrip, completedContext = vw.RootPathResolver(urlPath, context)
	name = vw.Name
	return
}

func (vw *HandlerBasedVirtualWorkspace) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if vw.Authorizer != nil {
		return vw.Authorizer(ctx, a)
	}
	return authorizer.DecisionNoOpinion, "", nil
}
