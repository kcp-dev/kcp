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
	"fmt"
	"net/http"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

func NewWorkspaceAuthenticator() authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		reqAuthenticator, ok := WorkspaceAuthenticatorFrom(req.Context())
		if !ok {
			return nil, false, nil
		}

		return reqAuthenticator.AuthenticateRequest(req)
	})
}

func withClusterScope(delegate authenticator.Request) authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		response, authenticated, ok := delegate.AuthenticateRequest(req)
		if authenticated {
			cluster := lookup.ClusterNameFrom(req.Context())

			extra := response.User.GetExtra()
			if extra == nil {
				extra = map[string][]string{}
			}
			extra["authentication.kcp.io/scopes"] = append(extra["authentication.kcp.io/scopes"], fmt.Sprintf("cluster:%s", cluster))
			extra["authentication.kcp.io/cluster-name"] = []string{cluster.String()}

			response.User = &user.DefaultInfo{
				Name:   response.User.GetName(),
				UID:    response.User.GetUID(),
				Groups: response.User.GetGroups(),
				Extra:  extra,
			}
		}

		return response, authenticated, ok
	})
}
