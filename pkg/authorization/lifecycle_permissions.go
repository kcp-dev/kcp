/*
Copyright 2026 The kcp Authors.

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

package authorization

import (
	"net/http"
	"net/url"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

// requestInfoFactory is shared by EvaluateLifecyclePermissions to extract verb /
// apiGroup / resource / resourceName / namespace from an incoming HTTP request.
var requestInfoFactory = requestinfo.NewFactory()

// EvaluateLifecyclePermissions checks whether the supplied user / HTTP request would
// be allowed by the given RBAC PolicyRules. It is the in-process RBAC evaluator used
// by the initializing/terminating virtual workspace content proxies when a
// WorkspaceType declares explicit initializerPermissions / terminatorPermissions.
//
// Behavior matches the standard Kubernetes RBAC matching semantics (verb / apiGroup /
// resource / resourceName, with non-resource URLs falling back to nonResourceURLs +
// verb matching).
//
// Returns (allowed, reason). The reason is suitable for inclusion in a 403 response
// body when allowed is false.
func EvaluateLifecyclePermissions(req *http.Request, u user.Info, rules []rbacv1.PolicyRule) (bool, string) {
	// The terminating/initializing VW handlers intentionally keep the kcp
	// "/clusters/<id>/" prefix on req.URL.Path so the request can be
	// reverse-proxied to the shard verbatim. Strip it here before computing
	// RequestInfo, since the standard k8s factory only recognizes paths that
	// start with /api or /apis.
	r := req
	if rest, ok := strings.CutPrefix(req.URL.Path, "/clusters/"); ok {
		if i := strings.Index(rest, "/"); i >= 0 {
			r = req.Clone(req.Context())
			r.URL = &url.URL{Path: rest[i:], RawQuery: req.URL.RawQuery}
		}
	}
	info, err := requestInfoFactory.NewRequestInfo(r)
	if err != nil {
		return false, "unable to parse request info"
	}

	attrs := authorizer.AttributesRecord{
		User:            u,
		Verb:            info.Verb,
		Namespace:       info.Namespace,
		APIGroup:        info.APIGroup,
		APIVersion:      info.APIVersion,
		Resource:        info.Resource,
		Subresource:     info.Subresource,
		Name:            info.Name,
		ResourceRequest: info.IsResourceRequest,
		Path:            info.Path,
	}

	if rbac.RulesAllow(attrs, rules...) {
		return true, ""
	}

	if attrs.ResourceRequest {
		return false, "no rule allows " + attrs.Verb + " on " + resourcePath(attrs)
	}
	return false, "no rule allows " + attrs.Verb + " on " + attrs.Path
}

// resourcePath formats a resource attribute set for inclusion in a 403 message.
func resourcePath(a authorizer.AttributesRecord) string {
	out := ""
	if a.APIGroup != "" {
		out += a.APIGroup + "/"
	} else {
		out += "core/"
	}
	out += a.Resource
	if a.Subresource != "" {
		out += "/" + a.Subresource
	}
	if a.Name != "" {
		out += " (" + a.Name + ")"
	}
	if a.Namespace != "" {
		out += " in namespace " + a.Namespace
	}
	return out
}

// RequestUserInfo is a convenience accessor that returns the authenticated user from
// the given request context, or nil if the context has no user attached.
func RequestUserInfo(req *http.Request) user.Info {
	u, _ := request.UserFrom(req.Context())
	return u
}
