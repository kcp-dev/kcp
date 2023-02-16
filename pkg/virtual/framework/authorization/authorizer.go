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

package authorization

import (
	"context"
	"fmt"

	"k8s.io/apiserver/pkg/authorization/authorizer"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

func NewVirtualWorkspaceAuthorizer(virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace) authorizer.Authorizer {
	return &virtualWorkspaceAuthorizer{
		virtualWorkspaces: virtualWorkspaces,
	}
}

var _ authorizer.Authorizer = (*virtualWorkspaceAuthorizer)(nil)

type virtualWorkspaceAuthorizer struct {
	virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace
}

func (a *virtualWorkspaceAuthorizer) Authorize(ctx context.Context, attrs authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	virtualWorkspaceName, _ := virtualcontext.VirtualWorkspaceNameFrom(ctx)
	if virtualWorkspaceName == "" {
		return authorizer.DecisionNoOpinion, "Path not resolved to a valid virtual workspace", nil
	}

	for _, vw := range a.virtualWorkspaces() {
		if vw.Name == virtualWorkspaceName {
			return vw.VirtualWorkspace.Authorize(ctx, attrs)
		}
	}

	// This should never happen if a virtual workspace name has been set in the context by the
	// ResolveRootPath method of one of the virtual workspaces.
	return authorizer.DecisionNoOpinion, "", fmt.Errorf("virtual Workspace %q not found", virtualWorkspaceName)
}
