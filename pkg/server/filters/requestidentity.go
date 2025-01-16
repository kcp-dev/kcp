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
	"context"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
)

type key int

var identityKey key

// WithIdentity adds an APIExport identity to the context.
func WithIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

// IdentityFromContext retrieves the APIExport identity from the context, if any.
func IdentityFromContext(ctx context.Context) string {
	s, _ := ctx.Value(identityKey).(string)
	return s
}

// WithResourceIdentity checks list/watch requests for an APIExport identity for the resource in the path.
// If it finds one (e.g. /api/v1/services:identityabcd1234/default/my-service), it places the identity from the path
// to the context, updates the request to remove the identity from the path, and updates requestInfo.Resource to also
// remove the identity. Finally, it hands off to the passed in handler to handle the request.
func WithResourceIdentity(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requestInfo, ok := request.RequestInfoFrom(req.Context())
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("missing requestInfo")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		updatedReq, err := processResourceIdentity(req, requestInfo)
		if err != nil {
			klog.FromContext(req.Context()).WithValues("operation", "WithRequestIdentity", "path", req.URL.Path).Error(err, "unable to determine resource")

			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(err),
				errorCodecs, schema.GroupVersion{}, w, req,
			)

			return
		}

		handler.ServeHTTP(w, updatedReq)
	})
}

func processResourceIdentity(req *http.Request, requestInfo *request.RequestInfo) (*http.Request, error) {
	if !requestInfo.IsResourceRequest {
		return req, nil
	}

	i := strings.Index(requestInfo.Resource, ":")

	if i < 0 {
		return req, nil
	}

	resource := requestInfo.Resource[:i]
	identity := requestInfo.Resource[i+1:]

	if identity == "" {
		return nil, fmt.Errorf("invalid resource %q: missing identity", resource)
	}

	req = utilnet.CloneRequest(req)

	req = req.WithContext(WithIdentity(req.Context(), identity))

	req.URL.Path = strings.Replace(req.URL.Path, requestInfo.Resource, resource, 1)
	req.URL.RawPath = strings.Replace(req.URL.RawPath, requestInfo.Resource, resource, 1)

	requestInfo.Resource = resource

	return req, nil
}
