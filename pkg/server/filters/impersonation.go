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

package filters

import (
	"fmt"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"

	authorizationbootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

type privilege int

const (
	unprivileged privilege = iota
	priviledged
	superPrivileged
)

var (
	// specialGroups specify groups with special meaning kcp. Lower privilege (= lower number)
	// cannot impersonate higher privilege levels.
	specialGroups = map[string]privilege{
		authorizationbootstrap.SystemMastersGroup:  superPrivileged,
		authorizationbootstrap.SystemKcpAdminGroup: priviledged,
	}
)

// WithImpersonationGatekeeper checks the request for impersonations and validates them,
// if they are valid. If they are not, will return a 403.
// We check for impersonation in the request headers, early to avoid it being propagated to
// the backend services.
func WithImpersonationGatekeeper(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Impersonation check is only done when impersonation is requested.
		// And impersonations is only allowed for the users, who have metadata in the ctx.
		// Else just pass the request.
		impersonationUser := req.Header.Get(authenticationv1.ImpersonateUserHeader)
		impersonationGroups := req.Header[authenticationv1.ImpersonateGroupHeader]
		impersonationExtras := []string{}
		for _, header := range req.Header {
			for _, h := range header {
				if strings.HasPrefix(h, authenticationv1.ImpersonateUserExtraHeaderPrefix) {
					impersonationExtras = append(impersonationExtras, h)
				}
			}
		}
		if len(impersonationUser) == 0 && len(impersonationGroups) == 0 && len(impersonationExtras) == 0 { // No user and group to impersonate - just pass the request.
			handler.ServeHTTP(w, req)
			return
		}

		requester, exists := request.UserFrom(req.Context())
		if !exists {
			responsewriters.ErrorNegotiated(
				apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("impersonation is invalid for the requestor")),
				errorCodecs, schema.GroupVersion{}, w, req)
			return
		}
		// TODO: Add scopes and warrants to the impersonation check.
		if validImpersonation(requester.GetGroups(), impersonationGroups) {
			handler.ServeHTTP(w, req)
			return
		}

		responsewriters.ErrorNegotiated(
			apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("impersonation is not allowed for the requestor")),
			errorCodecs, schema.GroupVersion{}, w, req)
	})
}

// maxUserPrivilege returns the highest privilege level found among the user's groups.
func maxUserPrivilege(userGroups []string) privilege {
	max := unprivileged
	for _, g := range userGroups {
		if p, found := specialGroups[g]; found && p > max {
			max = p
		}
	}
	return max
}

// validImpersonation checks if a user can impersonate all requested groups.
func validImpersonation(userGroups, requestedGroups []string) bool {
	userMax := maxUserPrivilege(userGroups)

	for _, g := range requestedGroups {
		if userMax < specialGroups[g] {
			return false
		}
	}
	return true
}
