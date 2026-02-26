/*
Copyright 2022 The kcp Authors.

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
	"errors"
	"net/http"
	"strings"

	"k8s.io/apiserver/pkg/authentication/authenticator"
)

// ForbidSystemUsernames wraps an authenticator and prevents it from returning
// an internal system username (anything beginning with "system:"). This is so
// per-workspace authenticators cannot impersonate low-level system accounts or
// serviceaccounts.
// This filter should be used together with the GroupsFilter to also strip
// system groups.
func ForbidSystemUsernames(delegate authenticator.Request) authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		result, authenticated, err := delegate.AuthenticateRequest(req)
		if err == nil {
			if strings.HasPrefix(result.User.GetName(), "system:") {
				return nil, false, errors.New("system usernames are not admitted")
			}
		}

		return result, authenticated, err
	})
}
