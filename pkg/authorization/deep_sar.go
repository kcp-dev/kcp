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

package authorization

import (
	"context"
	"fmt"
	"net/http"

	authorizationv1 "k8s.io/api/authorization/v1"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
)

type deepSARKeyType int

const (
	deepSARKey deepSARKeyType = iota
)

var (
	deepSARHeader = "X-Kcp-Internal-Deep-SubjectAccessReview"
)

// WithDeepSARConfig modifies and returns the input rest.Config
// with an additional header making SARs to be deep.
func WithDeepSARConfig(config *rest.Config) *rest.Config {
	config.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		return &withHeaderRoundtripper{
			RoundTripper: rt,
			headers: map[string]string{
				deepSARHeader: "true",
			},
		}
	})
	return config
}

type withHeaderRoundtripper struct {
	http.RoundTripper

	headers map[string]string
}

func (rt *withHeaderRoundtripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}
	return rt.RoundTripper.RoundTrip(req)
}

// WithDeepSubjectAccessReview attaches to the context that this request has set the DeepSubjectAccessReview
// header. The header is ignored for non-system:master users and for non-SAR request.
//
// A deep SAR request skips top-level workspace and workspace content authorization checks.
func WithDeepSubjectAccessReview(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		val := r.Header.Get(deepSARHeader)
		if val != "true" {
			handler.ServeHTTP(w, r)
			return
		}

		// only for SAR
		ri, ok := genericapirequest.RequestInfoFrom(r.Context())
		if !ok {
			responsewriters.InternalError(w, r, fmt.Errorf("cannot get request info"))
			return
		}
		if !ri.IsResourceRequest || ri.APIGroup != authorizationv1.GroupName || ri.Resource != "subjectaccessreviews" {
			handler.ServeHTTP(w, r)
			return
		}

		// only for privileged user
		user, ok := genericapirequest.UserFrom(r.Context())
		if !ok {
			responsewriters.InternalError(w, r, fmt.Errorf("cannot get user"))
			return
		}
		if !rbacregistryvalidation.EffectiveGroups(r.Context(), user).Has(kuser.SystemPrivilegedGroup) {
			handler.ServeHTTP(w, r)
			return
		}

		// attach whether that the header is set and all pre-conditions are fulfilled
		r = r.WithContext(context.WithValue(r.Context(), deepSARKey, true))
		handler.ServeHTTP(w, r)
	})
}

// IsDeepSubjectAccessReviewFrom returns whether this is a deep SAR request.
// If true, top-level workspace and workspace content authorization checks have to be skipped.
func IsDeepSubjectAccessReviewFrom(ctx context.Context, attr authorizer.Attributes) bool {
	k := ctx.Value(deepSARKey)
	return k != nil && k.(bool) && attr.GetAPIGroup() != authorizationv1.GroupName && attr.GetResource() != "subjectaccessreview"
}
