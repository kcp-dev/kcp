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

package chained

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/server/healthz"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
)

var _ framework.VirtualWorkspace = (*ChainedVirtualWorkspace)(nil)

type ChainedVirtualWorkspace struct {
	CommonPrefix string
	Chain        []framework.VirtualWorkspace
}

func (cvw *ChainedVirtualWorkspace) Names() framework.VirtualWorkspaceNames {
	var names []framework.VirtualWorkspaceName
	for _, vw := range cvw.Chain {
		names = append(names, vw.Names()...)
	}
	return names
}

func (cvw *ChainedVirtualWorkspace) HealthCheckers() []healthz.HealthChecker {
	var checks []healthz.HealthChecker
	for _, vw := range cvw.Chain {
		checks = append(checks, vw.HealthCheckers()...)
	}
	return checks
}

func (cvw *ChainedVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, name framework.VirtualWorkspaceName, completedContext context.Context) {
	completedContext = context

	if !strings.HasPrefix(urlPath, cvw.CommonPrefix) {
		return
	}

	for _, vw := range cvw.Chain {
		accepted, prefixToStrip, name, completedContext = vw.ResolveRootPath(urlPath, context)
		return
	}
	return
}

func (cvw *ChainedVirtualWorkspace) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	virtualWorkspaceName, _ := virtualcontext.VirtualWorkspaceNameFrom(ctx)
	if virtualWorkspaceName == "" {
		return authorizer.DecisionNoOpinion, "Path not resolved to a valid virtual workspace", nil
	}

	for _, vw := range cvw.Chain {
		if vw.Names().Has(virtualWorkspaceName) {
			return vw.Authorize(ctx, a)
		}
	}

	// This should never happen if a virtual workspace name has been set in the context by the
	// ResolveRootPath method of one of the virtual workspaces.
	return authorizer.DecisionNoOpinion, "", fmt.Errorf("Virtual Workspace %q not found", virtualWorkspaceName)
}
