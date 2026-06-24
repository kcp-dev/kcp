/*
Copyright 2025 The kcp Authors.

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
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"

	authorizationbootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

type privilege int

const (
	unprivileged privilege = iota
	authenticated
	privileged
	superPrivileged
)

var (
	// specialGroups specify groups with special meaning kcp. Lower privilege (= lower number)
	// cannot impersonate higher privilege levels.
	specialGroups = map[string]privilege{
		authorizationbootstrap.SystemMastersGroup:                superPrivileged,
		authorizationbootstrap.SystemLogicalClusterAdmin:         privileged,
		authorizationbootstrap.SystemExternalLogicalClusterAdmin: privileged,
		authorizationbootstrap.SystemKcpWorkspaceBootstrapper:    privileged,
		authorizationbootstrap.SystemKcpAdminGroup:               privileged,
		user.AllAuthenticated:                                    authenticated,
	}
)

// impersonationContextType is a context key for impersonation markers.
type impersonationContextType int

const (
	// impersonationContextKey is true if a request is impersonated.
	impersonationContextKey impersonationContextType = iota
	originalUserContextKey
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
		for header, headerValues := range req.Header {
			for _, h := range headerValues {
				if strings.HasPrefix(header, authenticationv1.ImpersonateUserExtraHeaderPrefix) {
					impersonationExtras = append(impersonationExtras, fmt.Sprintf("%s=%s", header, h))
				}
			}
		}

		// If no impersonation is requested, just pass the request.
		if len(impersonationUser) == 0 && len(impersonationGroups) == 0 && len(impersonationExtras) == 0 {
			// in withScoping, we will check that this value is set. If not, the is some programming error.
			ctx := req.Context()
			ctx = context.WithValue(ctx, impersonationContextKey, false)
			req = req.WithContext(ctx)

			handler.ServeHTTP(w, req)
			return
		}

		// remember that we impersonated for withScoping
		ctx := req.Context()
		ctx = context.WithValue(ctx, impersonationContextKey, true)

		requester, exists := request.UserFrom(ctx)
		if !exists {
			responsewriters.ErrorNegotiated(
				apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("impersonation is invalid for the requestor")),
				errorCodecs, schema.GroupVersion{}, w, req)
			return
		}
		ctx = context.WithValue(ctx, originalUserContextKey, requester)
		req = req.WithContext(ctx)

		// validImpersonation only inspects impersonated *groups*. Impersonation
		// extras such as the warrant and scope keys (authorization.kcp.io/warrant,
		// authentication.kcp.io/scopes, authentication.kcp.io/cluster-name) are
		// authority-granting and are populated only by trusted authenticators
		// server-side; a client must never be able to set them via an
		// Impersonate-Extra-* header. Trusted superusers (system:masters, e.g. the
		// in-process virtual-workspace client that legitimately attaches a warrant)
		// are exempt, mirroring the short-circuit in validImpersonation.
		if !sets.New(requester.GetGroups()...).Has(authorizationbootstrap.SystemMastersGroup) {
			if key, found := forbiddenImpersonationExtra(req.Header); found {
				responsewriters.ErrorNegotiated(
					apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("impersonating the extra key %q is not allowed for the requestor", key)),
					errorCodecs, schema.GroupVersion{}, w, req)
				return
			}
		}

		if validImpersonation(requester.GetGroups(), req.Header[authenticationv1.ImpersonateGroupHeader]) {
			handler.ServeHTTP(w, req)
			return
		}

		responsewriters.ErrorNegotiated(
			apierrors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("impersonation is not allowed for the requestor")),
			errorCodecs, schema.GroupVersion{}, w, req)
	})
}

// WithImpersonationScoping scopes the request to the cluster it is intended for.
func WithImpersonationScoping(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if impersonated, ok := req.Context().Value(impersonationContextKey).(bool); !ok {
			responsewriters.InternalError(w, req, fmt.Errorf("impersonation context not set"))
			return
		} else if !impersonated {
			// no impersonation, no scoping.
			handler.ServeHTTP(w, req)
			return
		}

		var (
			originalUser user.Info
			ok           bool
		)
		// fetch the original user from context. We stored this in WithImpersonationGatekeeper.
		if originalUser, ok = req.Context().Value(originalUserContextKey).(user.Info); !ok {
			responsewriters.InternalError(w, req, fmt.Errorf("no impersonating user in context"))
			return
		}

		// get impersonated user from context. Should always be there.
		u, exists := request.UserFrom(req.Context())
		if !exists {
			responsewriters.InternalError(w, req, fmt.Errorf("no user in context"))
			return
		}

		// system:masters can impersonate any group, without a scope.
		if sets.New(originalUser.GetGroups()...).Has(authorizationbootstrap.SystemMastersGroup) {
			handler.ServeHTTP(w, req)
			return
		}

		// scope to cluster because impersonation happened.
		cluster := request.ClusterFrom(req.Context())
		if cluster == nil {
			responsewriters.InternalError(w, req, fmt.Errorf("no cluster in context"))
			return
		}

		// add a scope to the user information.
		extra := u.GetExtra()
		if extra == nil {
			extra = map[string][]string{}
		}
		extra["authentication.kcp.io/scopes"] = append(extra["authentication.kcp.io/scopes"], fmt.Sprintf("cluster:%s", cluster.Name))

		userScoped := &user.DefaultInfo{
			Name:   u.GetName(),
			UID:    u.GetUID(),
			Groups: u.GetGroups(),
			Extra:  extra,
		}

		handler.ServeHTTP(w, req.WithContext(request.WithUser(req.Context(), userScoped)))
	})
}

// forbiddenImpersonationExtra reports whether the request carries an
// Impersonate-Extra-* header for a kcp-reserved extra key (any key containing
// "kcp.io", e.g. authorization.kcp.io/warrant or authentication.kcp.io/scopes).
// Such extras grant authority and must only ever be set by trusted
// authenticators, never asserted by a client. The header key suffix is
// percent-encoded by clients (e.g. authorization.kcp.io%2Fwarrant), so it is
// unescaped before matching. This mirrors the ExtraFilter DropExtraKeyContains
// applied to workspace-local authenticators in pkg/authentication.
func forbiddenImpersonationExtra(header http.Header) (string, bool) {
	for name := range header {
		if !strings.HasPrefix(name, authenticationv1.ImpersonateUserExtraHeaderPrefix) {
			continue
		}
		raw := name[len(authenticationv1.ImpersonateUserExtraHeaderPrefix):]
		key, err := url.PathUnescape(raw)
		if err != nil {
			key = raw
		}
		if strings.Contains(strings.ToLower(key), "kcp.io") {
			return key, true
		}
	}
	return "", false
}

// validImpersonation checks if a user can impersonate all requested groups.
func validImpersonation(existingGroups, requestedGroups []string) bool {
	for _, g := range existingGroups {
		if g == authorizationbootstrap.SystemMastersGroup {
			return true
		}
	}

	existing := sets.New(existingGroups...)
	for _, g := range requestedGroups {
		if specialGroups[g] != unprivileged && !existing.Has(g) {
			return false // only impersonate non-unprivileged groups the user already has.
		}
	}

	return true
}
