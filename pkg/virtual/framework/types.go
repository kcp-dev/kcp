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

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

// RootPathResolverFunc is the type of a function that, based on the URL path of a request,
// returns whether the request should be accepted and served by a given VirtuaWorkspace.
// When it returns `true`, it will also set the VirtualWorkspace name in the request context,
// as well as the prefix that should be removed from the request URL Path before forwarding
// the request to the given VirtualWorkspace delegated APIServer.
type RootPathResolverFunc func(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context)

func (r RootPathResolverFunc) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	return r(urlPath, context)
}

var _ RootPathResolver = RootPathResolverFunc(nil)

type RootPathResolver interface {
	// ResolveRootPath returns whether the request should be accepted and served by a given VirtualWorkspace. If accepted,
	// the prefixToStrip is the prefix that is in-front of the kube-like API surface (including `/clusters/<something>`). E.g. for
	//
	//   /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.io/v1alpha1/synctargets
	//
	// the prefixToStrip is `/services/initializingworkspaces/<initializer/clusters/<something>`.
	//
	// Depending on virtual workspace type, the returned context holds e.g. the APIDomainKey, the logical cluster or
	// other values like scope, if that is part of the URL path.
	ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context)
}

// ReadyFunc is the type of readiness check functions exposed by types
// implementing the VtualWorkspace interface.
type ReadyFunc func() error

func (r ReadyFunc) IsReady() error {
	return r()
}

var _ ReadyChecker = ReadyFunc(nil)

type ReadyChecker interface {
	IsReady() error
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
	RootPathResolver
	ReadyChecker
	Register(name string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error)
}
