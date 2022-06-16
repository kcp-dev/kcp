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

package handlerbased

import (
	"errors"
	"net/http"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

func (vw *HandlerBasedVirtualWorkspace) Register(rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	handler, err := vw.BootstrapHandler(rootAPIServerConfig)
	if err != nil {
		return nil, err
	}
	return genericapiserver.NewEmptyDelegateWithCustomHandler(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		vwName, found := virtualcontext.VirtualWorkspaceNameFrom(r.Context())
		if !found {
			utilruntime.HandleError(errors.New("context should always contain a virtual workspace name when hitting a virtual workspace delegated APIServer"))
			http.NotFoundHandler().ServeHTTP(rw, r)
			return
		}
		if vwName == vw.Name {
			handler.ServeHTTP(rw, r)
			return
		}
		delegatedHandler := delegateAPIServer.UnprotectedHandler()
		if delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
		} else {
			http.NotFoundHandler().ServeHTTP(rw, r)
		}
	})), nil
}
