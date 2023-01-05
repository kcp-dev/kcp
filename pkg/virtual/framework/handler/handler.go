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

package handler

import (
	"net/http"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
)

// HandlerFactory creates an HTTP handler using the root API server configuration, for dynamic registration.
type HandlerFactory func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error)

type VirtualWorkspace struct {
	framework.RootPathResolver
	authorizer.Authorizer
	framework.ReadyChecker
	HandlerFactory
}

func (v *VirtualWorkspace) Register(vwName string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	handler, err := v.HandlerFactory(rootAPIServerConfig)
	if err != nil {
		return nil, err
	}
	return genericapiserver.NewEmptyDelegateWithCustomHandler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if ctxName, found := virtualcontext.VirtualWorkspaceNameFrom(r.Context()); found && ctxName == vwName {
			handler.ServeHTTP(rw, r)
			return
		}
		if delegatedHandler := delegateAPIServer.UnprotectedHandler(); delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
			return
		}
		http.NotFoundHandler().ServeHTTP(rw, r)
	})), nil
}

var _ framework.VirtualWorkspace = &VirtualWorkspace{}
