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

package apiserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/metrics"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/registry/rest"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kube-openapi/pkg/validation/spec"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// resourceHandler serves the `/apis` and `/api` endpoints.
type resourceHandler struct {
	apiSetRetriever         apidefinition.APIDefinitionSetGetter
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

func newResourceHandler(
	apiSetRetriever apidefinition.APIDefinitionSetGetter,
	versionDiscoveryHandler *versionDiscoveryHandler,
	groupDiscoveryHandler *groupDiscoveryHandler,
	rootDiscoveryHandler *rootDiscoveryHandler,
	delegate http.Handler,
	admission admission.Interface,
	authorizer authorizer.Authorizer,
	requestTimeout time.Duration,
	minRequestTimeout time.Duration,
	maxRequestBodyBytes int64,
	staticOpenAPISpec *spec.Swagger) (*resourceHandler, error) {
	ret := &resourceHandler{
		apiSetRetriever:         apiSetRetriever,
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

func (r *resourceHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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

	apiDefs, hasLocationKey, err := r.apiSetRetriever.GetAPIDefinitionSet(ctx, locationKey)
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

	apiDef, hasAPIDef := apiDefs[schema.GroupVersionResource{
		Group:    requestInfo.APIGroup,
		Version:  requestInfo.APIVersion,
		Resource: requestInfo.Resource,
	}]
	if !hasAPIDef {
		r.delegate.ServeHTTP(w, req)
		return
	}

	apiResourceSchema := apiDef.GetAPIResourceSchema()
	var apiResourceVersion *apisv1alpha1.APIResourceVersion
	for i := range apiResourceSchema.Spec.Versions {
		if v := &apiResourceSchema.Spec.Versions[i]; v.Name == requestInfo.APIVersion {
			apiResourceVersion = v
			break
		}
	}
	if apiResourceVersion == nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("unable to find API version %q in API definition", requestInfo.APIVersion)),
			errorCodecs, schema.GroupVersion{},
			w, req)
		return
	}

	verb := strings.ToUpper(requestInfo.Verb)
	resource := requestInfo.Resource
	subresource := requestInfo.Subresource
	scope := metrics.CleanScope(requestInfo)
	supportedTypes := []string{
		string(types.JSONPatchType),
		string(types.MergePatchType),
	}

	// HACK: Support resources of the client-go scheme the way existing clients expect it:
	//   - Support Strategic Merge Patch (used by default on these resources by kubectl)
	//   - Support the Protobuf content type on Create / Update resources
	//     (by simply converting the request to the json content type),
	//     since protobuf content type is expected to be supported in a number of client
	//     contexts (like controller-runtime for example)
	if kubernetesscheme.Scheme.IsGroupRegistered(requestInfo.APIGroup) {
		supportedTypes = append(supportedTypes, string(types.StrategicMergePatchType))
		req, err := apiextensionsapiserver.ConvertProtobufRequestsToJson(verb, req, schema.GroupVersionKind{
			Group:   requestInfo.APIGroup,
			Version: requestInfo.APIVersion,
			Kind:    apiResourceSchema.Spec.Names.Kind,
		})
		if err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(err),
				codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
			)
			return
		}
	}

	if kcpfeatures.DefaultFeatureGate.Enabled(features.ServerSideApply) {
		supportedTypes = append(supportedTypes, string(types.ApplyPatchType))
	}

	var handlerFunc http.HandlerFunc
	subresources := apiResourceVersion.Subresources
	switch {
	case subresource == "status" && subresources.Status != nil:
		handlerFunc = r.serveStatus(w, req, requestInfo, apiDef, supportedTypes)
	case len(subresource) == 0:
		handlerFunc = r.serveResource(w, req, requestInfo, apiDef, supportedTypes)
	default:
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Name),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
	}

	if handlerFunc != nil {
		handlerFunc = metrics.InstrumentHandlerFunc(verb, requestInfo.APIGroup, requestInfo.APIVersion, resource, subresource, scope, metrics.APIServerComponent, false, "", handlerFunc)
		handlerFunc.ServeHTTP(w, req)
		return
	}
}

func (r *resourceHandler) serveResource(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, apiDef apidefinition.APIDefinition, supportedTypes []string) http.HandlerFunc {
	requestScope := apiDef.GetRequestScope()
	storage := apiDef.GetStorage()

	switch requestInfo.Verb {
	case "get":
		if storage, isAble := storage.(rest.Getter); isAble {
			return handlers.GetResource(storage, requestScope)
		}
	case "list":
		if listerStorage, isAble := storage.(rest.Lister); isAble {
			if watcherStorage, isAble := storage.(rest.Watcher); isAble {
				forceWatch := false
				return handlers.ListResource(listerStorage, watcherStorage, requestScope, forceWatch, r.minRequestTimeout)
			}
		}
	case "watch":
		if listerStorage, isAble := storage.(rest.Lister); isAble {
			if watcherStorage, isAble := storage.(rest.Watcher); isAble {
				forceWatch := true
				return handlers.ListResource(listerStorage, watcherStorage, requestScope, forceWatch, r.minRequestTimeout)
			}
		}
	case "create":
		if storage, isAble := storage.(rest.Creater); isAble {
			return handlers.CreateResource(storage, requestScope, r.admission)
		}
	case "update":
		if storage, isAble := storage.(rest.Updater); isAble {
			return handlers.UpdateResource(storage, requestScope, r.admission)
		}
	case "patch":
		if storage, isAble := storage.(rest.Patcher); isAble {
			return handlers.PatchResource(storage, requestScope, r.admission, supportedTypes)
		}
	case "delete":
		if storage, isAble := storage.(rest.GracefulDeleter); isAble {
			allowsOptions := true
			return handlers.DeleteResource(storage, allowsOptions, requestScope, r.admission)
		}
	case "deletecollection":
		if storage, isAble := storage.(rest.CollectionDeleter); isAble {
			checkBody := true
			return handlers.DeleteCollection(storage, checkBody, requestScope, r.admission)
		}
	}
	responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
}

func (r *resourceHandler) serveStatus(w http.ResponseWriter, req *http.Request, requestInfo *apirequest.RequestInfo, apiDef apidefinition.APIDefinition, supportedTypes []string) http.HandlerFunc {
	requestScope := apiDef.GetSubResourceRequestScope("status")
	storage := apiDef.GetSubResourceStorage("status")

	switch requestInfo.Verb {
	case "get":
		if storage, isAble := storage.(rest.Getter); isAble {
			return handlers.GetResource(storage, requestScope)
		}
	case "update":
		if storage, isAble := storage.(rest.Updater); isAble {
			return handlers.UpdateResource(storage, requestScope, r.admission)
		}
	case "patch":
		if storage, isAble := storage.(rest.Patcher); isAble {
			return handlers.PatchResource(storage, requestScope, r.admission, supportedTypes)
		}
	}
	responsewriters.ErrorNegotiated(
		apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
		codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
	)
	return nil
}
