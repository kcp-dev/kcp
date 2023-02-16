/*
Copyright 2023 The KCP Authors.

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

package rootapiserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/warning"
	componentbaseversion "k8s.io/component-base/version"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

// Server is only responsible for serving the APIs for the virtual workspace
// at a given root path or root path family
// It does NOT expose oauth, related oauth endpoints, or any kube APIs.
type Server struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

func NewServer(c CompletedConfig, delegationTarget genericapiserver.DelegationTarget) (*Server, error) {
	delegateAPIServer := delegationTarget
	for _, vw := range c.Extra.VirtualWorkspaces {
		var err error
		delegateAPIServer, err = vw.Register(vw.Name, c.Generic, delegateAPIServer)
		if err != nil {
			return nil, err
		}
	}

	c.Generic.BuildHandlerChainFunc = getRootHandlerChain(c, delegateAPIServer)
	c.Generic.ReadyzChecks = append(c.Generic.ReadyzChecks, asHealthChecks(c.Extra.VirtualWorkspaces)...)

	genericServer, err := c.Generic.New("virtual-workspaces-root-apiserver", delegateAPIServer)
	if err != nil {
		return nil, err
	}

	s := &Server{
		GenericAPIServer: genericServer,
	}

	return s, nil
}

func getRootHandlerChain(c CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) func(http.Handler, *genericapiserver.Config) http.Handler {
	return func(apiHandler http.Handler, genericConfig *genericapiserver.Config) http.Handler {
		delegateAfterDefaultHandlerChain := genericapiserver.DefaultBuildHandlerChain(
			http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if _, virtualWorkspaceNameExists := virtualcontext.VirtualWorkspaceNameFrom(req.Context()); virtualWorkspaceNameExists {
					delegatedHandler := delegateAPIServer.UnprotectedHandler()
					if delegatedHandler != nil {
						delegatedHandler.ServeHTTP(w, req)
					}
					return
				}
				apiHandler.ServeHTTP(w, req)
			}), c.Generic.Config)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requestContext := req.Context()
			// detect old kubectl plugins and inject warning headers
			if req.UserAgent() == "Go-http-client/2.0" {
				// TODO(sttts): in the future compare the plugin version to the server version and warn outside of skew compatibility guarantees.
				warning.AddWarning(requestContext, "",
					fmt.Sprintf("You are using an old kubectl-kcp plugin. Please update to a version matching the kcp server version %q.", componentbaseversion.Get().GitVersion))
			}

			for _, vw := range c.Extra.VirtualWorkspaces {
				if accepted, prefixToStrip, completedContext := vw.ResolveRootPath(req.URL.Path, requestContext); accepted {
					req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixToStrip)
					newURL, err := url.Parse(req.URL.String())
					if err != nil {
						responsewriters.ErrorNegotiated(
							apierrors.NewInternalError(fmt.Errorf("unable to resolve %s, err %w", req.URL.Path, err)),
							errorCodecs, schema.GroupVersion{},
							w, req)
						return
					}
					req.URL = newURL
					req = req.WithContext(virtualcontext.WithVirtualWorkspaceName(completedContext, vw.Name))
					break
				}
			}
			delegateAfterDefaultHandlerChain.ServeHTTP(w, req)
		})
	}
}
