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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
)

// WithWildcardListWatchGuard fails wildcard requests on everything but list and watch verbs.
func WithWildcardListWatchGuard(apiHandler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cluster := request.ClusterFrom(req.Context())

		if cluster != nil && cluster.Wildcard {
			requestInfo, ok := request.RequestInfoFrom(req.Context())
			if !ok {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(fmt.Errorf("missing requestInfo")),
					errorCodecs, schema.GroupVersion{}, w, req,
				)

				return
			}

			if requestInfo.IsResourceRequest && !sets.New[string]("list", "watch").Has(requestInfo.Verb) {
				statusErr := apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb)
				statusErr.ErrStatus.Message += " in the `*` logical cluster"

				responsewriters.ErrorNegotiated(
					statusErr,
					errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
				)

				return
			}
		}

		apiHandler.ServeHTTP(w, req)
	}
}
