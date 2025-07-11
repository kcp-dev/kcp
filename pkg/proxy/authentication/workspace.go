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

package authentication

import (
	"net/http"

	"k8s.io/apiserver/pkg/authentication/authenticator"

	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

type WorkspaceAuthenticator struct {
	delegate  authenticator.Request
	authIndex AuthenticatorIndex
}

func NewWorkspaceAuthenticator(delegate authenticator.Request, authIndex AuthenticatorIndex) *WorkspaceAuthenticator {
	return &WorkspaceAuthenticator{
		delegate:  delegate,
		authIndex: authIndex,
	}
}

var _ authenticator.Request = &WorkspaceAuthenticator{}

func (a *WorkspaceAuthenticator) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	response, authenticated, err := a.delegate.AuthenticateRequest(req)
	if err != nil || authenticated {
		return response, authenticated, err
	}

	wsType := lookup.WorkspaceTypeFrom(req.Context())
	if wsType == nil {
		return nil, false, nil
	}

	authenticator, ok := a.authIndex.Lookup(*wsType)
	if !ok {
		return nil, false, nil
	}

	return authenticator.AuthenticateRequest(req)
}
