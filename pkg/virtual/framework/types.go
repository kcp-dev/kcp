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

package framework

import (
	"context"
	"net/http"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/healthz"
)

type VirtualWorkspaceName string
type VirtualWorkspaceNames []VirtualWorkspaceName

func (names VirtualWorkspaceNames) Has(name VirtualWorkspaceName) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// RootPathResolverFunc is the type of a function that, based on the URL path of a request,
// returns whether the request should be accepted and served by a given VirtuaWorkspace.
// When it returns `true`, it will also set the VirtualWorkspace name in the request context,
// as well as the prefix that should be removed from the request URL Path before forwarding
// the request to the given VirtualWorkspace delegated APIServer.
type RootPathResolverFunc func(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context)

// ReadyFunc is the type of readiness check functions exposed by types
// implementing the VtualWorkspace interface.
type ReadyFunc func() error

func (ready ReadyFunc) HealthCheck(name VirtualWorkspaceName) healthz.HealthChecker {
	return healthz.NamedCheck(string(name), func(r *http.Request) error {
		return ready()
	})
}

// VirtualWorkspace is the definition of a virtual workspace
// that will be registered and made available, at a given prefix,
// inside a Root API server as a delegated API Server.
//
// It will be implemented by several types of virtual workspaces.
//
// One example is the FixedGroupVersionsVirtualWorkspace located in the
// fixedgvs package, which allows adding well-defined APIs
// in a limited number of group/versions, implemented as Rest storages.
type VirtualWorkspace interface {
	authorizer.Authorizer
	Names() VirtualWorkspaceNames
	ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, name VirtualWorkspaceName, completedContext context.Context)
	HealthCheckers() []healthz.HealthChecker
	Register(rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error)
	Authorize(context.Context, authorizer.Attributes) (authorizer.Decision, string, error)
}
