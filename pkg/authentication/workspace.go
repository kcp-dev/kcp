/*
Copyright 2025 The KCP Authors.

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
)

type workspaceAuthenticator struct{}

func NewWorkspaceAuthenticator() authenticator.Request {
	return &workspaceAuthenticator{}
}

func (a *workspaceAuthenticator) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	reqAuthenticator, ok := WorkspaceAuthenticatorFrom(req.Context())
	if !ok {
		return nil, false, nil
	}

	return reqAuthenticator.AuthenticateRequest(req)
}
