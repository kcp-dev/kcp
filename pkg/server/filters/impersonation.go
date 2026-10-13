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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	"github.com/kcp-dev/kcp/pkg/authentication"
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
		// If no impersonation is requested, just pass the request. This asks the
		// same question the apiserver's impersonation filter asks, so a request it
		// would treat as impersonating cannot slip past the checks below.
		if !authentication.HasImpersonationHeaders(req.Header) {
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

		// Don't allow non-system:masters to impersonate reserved kcp extra keys.
		// Scopes, warrants and the service account cluster name are exempt here:
		// scopes can only narrow access, and warrants and cluster names are
		// confined to the request's cluster in WithImpersonationScoping.
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

		// A service account cluster name is an identity claim, not a restriction:
		// the authorizers derive the global identity
		// system:kcp:serviceaccount:<cluster-name>:<ns>:<name> from it regardless
		// of scopes. A client may therefore only claim the cluster it is acting in.
		if err := checkClusterName(u.GetExtra(), cluster.Name.String()); err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewForbidden(schema.GroupResource{}, "", err),
				errorCodecs, schema.GroupVersion{}, w, req)
			return
		}

		// add a scope to the user information. Copy the extra map so the
		// authenticated user.Info is not mutated.
		scope := fmt.Sprintf("cluster:%s", cluster.Name)
		extra := make(map[string][]string, len(u.GetExtra())+1)
		for k, v := range u.GetExtra() {
			extra[k] = append([]string(nil), v...)
		}
		extra[rbacregistryvalidation.ScopeExtraKey] = append(extra[rbacregistryvalidation.ScopeExtraKey], scope)

		// Warrants are evaluated independently of the primary user's scopes, so
		// a client-supplied warrant must be scoped as well. Otherwise a user
		// with impersonation rights in one workspace could carry a warrant that
		// grants access in every other workspace.
		if warrants, ok := extra[rbacregistryvalidation.WarrantExtraKey]; ok {
			scoped, err := scopeWarrants(warrants, scope, cluster.Name.String())
			if err != nil {
				responsewriters.ErrorNegotiated(
					apierrors.NewForbidden(schema.GroupResource{}, "", err),
					errorCodecs, schema.GroupVersion{}, w, req)
				return
			}
			extra[rbacregistryvalidation.WarrantExtraKey] = scoped
		}

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
// Impersonate-Extra-* header for a kcp-reserved extra key, i.e. any key
// containing "kcp.io" other than the ones WithImpersonationScoping handles:
// scopes only narrow access, warrants get scoped to the request's cluster,
// and a service account cluster name must match the request's cluster.
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
		lower := strings.ToLower(key)
		switch lower {
		case rbacregistryvalidation.ScopeExtraKey, rbacregistryvalidation.WarrantExtraKey, serviceaccount.ClusterNameKey:
			continue
		}
		if strings.Contains(lower, "kcp.io") {
			return key, true
		}
	}
	return "", false
}

// checkClusterName returns an error if the extra carries a service account
// cluster name other than the given cluster. Scoping cannot neutralize a
// foreign cluster name, because the global service account identity derived
// from it is not subject to scopes.
func checkClusterName(extra map[string][]string, clusterName string) error {
	for _, v := range extra[serviceaccount.ClusterNameKey] {
		if v != clusterName {
			return fmt.Errorf("impersonating the extra key %q with value %q is not allowed for the requestor in cluster %q", serviceaccount.ClusterNameKey, v, clusterName)
		}
	}
	return nil
}

// scopeWarrants adds the given scope to every warrant and, recursively, to
// every nested warrant, so that no warrant can grant access outside the
// cluster the impersonation happened in. Scopes intersect, so an already
// narrower scope stays in effect. Warrants that cannot be decoded are dropped.
// A warrant claiming a service account cluster name other than the given
// cluster is rejected, see checkClusterName.
func scopeWarrants(warrants []string, scope, clusterName string) ([]string, error) {
	out := make([]string, 0, len(warrants))
	for _, raw := range warrants {
		var w rbacregistryvalidation.Warrant
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			continue
		}
		if err := checkClusterName(w.Extra, clusterName); err != nil {
			return nil, fmt.Errorf("warrant: %w", err)
		}
		if w.Extra == nil {
			w.Extra = map[string][]string{}
		}
		w.Extra[rbacregistryvalidation.ScopeExtraKey] = append(w.Extra[rbacregistryvalidation.ScopeExtraKey], scope)
		if nested, ok := w.Extra[rbacregistryvalidation.WarrantExtraKey]; ok {
			scopedNested, err := scopeWarrants(nested, scope, clusterName)
			if err != nil {
				return nil, err
			}
			w.Extra[rbacregistryvalidation.WarrantExtraKey] = scopedNested
		}
		bs, err := json.Marshal(w)
		if err != nil {
			continue
		}
		out = append(out, string(bs))
	}
	return out, nil
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
