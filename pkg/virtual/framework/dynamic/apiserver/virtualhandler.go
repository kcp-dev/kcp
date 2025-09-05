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

package apiserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/metrics"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kube-openapi/pkg/validation/spec"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/virtualapidefinition"
)

// virtualResourceHandler serves the `/apis` and `/api` endpoints.
type virtualResourceHandler struct {
	virtApiSetRetriever     virtualapidefinition.VirtualAPIDefinitionSetGetter
	versionDiscoveryHandler *versionDiscoveryHandler
	groupDiscoveryHandler   *groupDiscoveryHandler
	rootDiscoveryHandler    *rootDiscoveryHandler

	delegate http.Handler

	admission admission.Interface

	// so that we can do create on update.
	authorizer authorizer.Authorizer

	// request timeout we should delay storage teardown for
	requestTimeout time.Duration

	// minRequestTimeout applies to list/watch calls
	minRequestTimeout time.Duration

	// The limit on the request size that would be accepted and decoded in a write request
	// 0 means no limit.
	maxRequestBodyBytes int64
}

func newVirtualResourceHandler(
	virtApiSetRetriever virtualapidefinition.VirtualAPIDefinitionSetGetter,
	versionDiscoveryHandler *versionDiscoveryHandler,
	groupDiscoveryHandler *groupDiscoveryHandler,
	rootDiscoveryHandler *rootDiscoveryHandler,
	delegate http.Handler,
	admission admission.Interface,
	authorizer authorizer.Authorizer,
	requestTimeout time.Duration,
	minRequestTimeout time.Duration,
	maxRequestBodyBytes int64,
	staticOpenAPISpec *spec.Swagger) (*virtualResourceHandler, error) {
	ret := &virtualResourceHandler{
		virtApiSetRetriever:     virtApiSetRetriever,
		versionDiscoveryHandler: versionDiscoveryHandler,
		groupDiscoveryHandler:   groupDiscoveryHandler,
		rootDiscoveryHandler:    rootDiscoveryHandler,
		delegate:                delegate,
		admission:               admission,
		authorizer:              authorizer,
		requestTimeout:          requestTimeout,
		minRequestTimeout:       minRequestTimeout,
		maxRequestBodyBytes:     maxRequestBodyBytes,
	}

	return ret, nil
}

func (r *virtualResourceHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	requestInfo, ok := apirequest.RequestInfoFrom(ctx)

	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}
	if !requestInfo.IsResourceRequest {
		pathParts := splitPath(requestInfo.Path)
		// only match /apis/<group>/<version> or /api/<version>
		if len(pathParts) == 3 && pathParts[0] == "apis" ||
			len(pathParts) == 2 && pathParts[0] == "api" {
			r.versionDiscoveryHandler.ServeHTTP(w, req)
			return
		}
		// only match /apis/<group> or /api
		if len(pathParts) == 2 && pathParts[0] == "apis" ||
			len(pathParts) == 1 && pathParts[0] == "api" {
			r.groupDiscoveryHandler.ServeHTTP(w, req)
			return
		}
		// only match /apis
		if len(pathParts) == 1 && pathParts[0] == "apis" {
			r.rootDiscoveryHandler.ServeHTTP(w, req)
			return
		}

		r.delegate.ServeHTTP(w, req)
		return
	}

	locationKey := dynamiccontext.APIDomainKeyFrom(ctx)

	virtApiDefs, hasLocationKey, err := r.virtApiSetRetriever.GetVirtualAPIDefinitionSet(ctx, locationKey)
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("unable to determine API definition set: %w", err)),
			errorCodecs, schema.GroupVersion{},
			w, req)
		return
	}
	if !hasLocationKey {
		r.delegate.ServeHTTP(w, req)
		return
	}

	virtApiDef, hasAPIDef := virtApiDefs[schema.GroupResource{
		Group:    requestInfo.APIGroup,
		Resource: requestInfo.Resource,
	}]
	if !hasAPIDef {
		r.delegate.ServeHTTP(w, req)
		return
	}

	verb := strings.ToUpper(requestInfo.Verb)
	resource := requestInfo.Resource
	subresource := requestInfo.Subresource
	scope := metrics.CleanScope(requestInfo)
	supportedTypes := []string{
		string(types.JSONPatchType),
		string(types.MergePatchType),
		string(types.ApplyPatchType),
	}

	handlerFunc := metrics.InstrumentHandlerFunc(verb, requestInfo.APIGroup, requestInfo.APIVersion, resource, subresource, scope, metrics.APIServerComponent, false, "", r.serveResource(w, req, requestInfo, virtApiDef, supportedTypes))
	handlerFunc.ServeHTTP(w, req)
}

func (r *virtualResourceHandler) serveResource(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, virtApiDef virtualapidefinition.VirtualAPIDefinition, supportedTypes []string) http.HandlerFunc {
	proxy, err := virtApiDef.GetProxy(req.Context())
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return nil
	}

	return proxy.ServeHTTP

	/*responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
	*/
}
