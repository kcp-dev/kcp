/*
Copyright 2021 The kcp Authors.

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
	"strings"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/admission"
)

const (
	// ClusterWildcard names the http.ServeMux wildcard of a virtual workspace path pattern that holds the logical cluster.
	ClusterWildcard = "cluster"
	// TrailWildcard names the http.ServeMux wildcard holding the kube-like API path after the virtual workspace path pattern.
	TrailWildcard = "trail"
)

// PathMatch is a request path matched against a virtual workspace pattern.
type PathMatch struct {
	// Path is the full request path.
	Path string
	// Trail is the kube-like API path after the pattern, "" if none.
	Trail string

	value func(name string) string
}

// NewPathMatch returns a PathMatch with the given wildcard values.
func NewPathMatch(path, trail string, values map[string]string) PathMatch {
	return PathMatch{
		Path:  path,
		Trail: trail,
		value: func(name string) string {
			return values[name]
		},
	}
}

// PathMatchFromRequest returns the PathMatch of a request routed by a
// http.ServeMux pattern with a trailing TrailWildcard.
func PathMatchFromRequest(req *http.Request) PathMatch {
	return PathMatch{
		Path:  req.URL.Path,
		Trail: req.PathValue(TrailWildcard),
		value: req.PathValue,
	}
}

// Value returns the segment matched by the named wildcard of the pattern,
// "" if there is none.
func (m PathMatch) Value(name string) string {
	if m.value == nil {
		return ""
	}
	return m.value(name)
}

// PrefixToStrip returns the path up to the trail, without the separator,
// the default prefix to strip before delegating to the virtual workspace.
// A path ending in the separator keeps it as the remaining path.
func (m PathMatch) PrefixToStrip() string {
	if m.Trail == "" {
		return strings.TrimSuffix(m.Path, "/")
	}
	return strings.TrimSuffix(m.Path, "/"+m.Trail)
}

// RootPathResolverFunc adapts a function to RootPathResolver.
type RootPathResolverFunc func(ctx context.Context, match PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context)

func (r RootPathResolverFunc) ResolveRootPath(ctx context.Context, match PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	return r(ctx, match)
}

var _ RootPathResolver = RootPathResolverFunc(nil)

type RootPathResolver interface {
	// ResolveRootPath returns whether the request should be accepted and served by a given VirtualWorkspace.
	//
	// match holds the wildcards of the virtual workspace's path pattern;
	// the logical cluster named by ClusterWildcard is already set on ctx.
	//
	// If accepted, prefixToStrip is the prefix in front of the kube-like API
	// surface (including `/clusters/<something>`), usually match.PrefixToStrip().
	// E.g. for
	//
	//   /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.io/v1alpha1/synctargets
	//
	// the prefixToStrip is `/services/initializingworkspaces/<initializer/clusters/<something>`.
	//
	// Depending on virtual workspace type, the returned context holds e.g. the APIDomainKey
	// or other values like scope, if that is part of the URL path.
	ResolveRootPath(ctx context.Context, match PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context)
}

// ReadyFunc is the type of readiness check functions exposed by types
// implementing the VirtualWorkspace interface.
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
	admission.Mutator
	admission.Validator
	RootPathResolver
	ReadyChecker
	Register(name string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error)
}
