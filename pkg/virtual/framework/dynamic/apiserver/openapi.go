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

	"github.com/emicklei/go-restful/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/kube-openapi/pkg/builder3"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/common/restfuladapter"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dyncamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

/*
*
openAPIHandler implements the OpenAPI v3 handler for virtual workspaces.

This handler generates the OpenAPI v3 spec by:

  - Creating specs based on webservices registered with the virtual workspace API server. These are only /api, /apis,
    and /version, without any subroute (e.g. /apis/<group>)
  - Creating a webservice for every /apis/<group> route available by utilizing the API discovery, then generating
    OpenAPI v3 specs from these webservices
  - Converting every APIDefinition into a CustomResourceDefinition and generating OpenAPI v3 specs for
    /apis/<group>/<version> from these CRDs using the kube-openapi library

This is different to the regular API servers, which are generating the OpenAPI v3 spec by purely using the webservices.
This is not an option here because the virtual workspace API server has only three webservices registered
(/api, /apis, and /version, without any subroute).
*/
type openAPIHandler struct {
	apiSetRetriever    apidefinition.APIDefinitionSetGetter
	goRestfulContainer *restful.Container
	openapiv3Config    *common.OpenAPIV3Config
	delegate           http.Handler

	openAPIV3Service *handler3.OpenAPIService
}

func newOpenAPIHandler(
	apiSetRetriever apidefinition.APIDefinitionSetGetter,
	goRestfulContainer *restful.Container,
	openapiv3Config *common.OpenAPIV3Config,
	delegate http.Handler) *openAPIHandler {
	openAPIV3Service := handler3.NewOpenAPIService()

	return &openAPIHandler{
		apiSetRetriever:    apiSetRetriever,
		goRestfulContainer: goRestfulContainer,
		openapiv3Config:    openapiv3Config,
		delegate:           delegate,

		openAPIV3Service: openAPIV3Service,
	}
}

func (o *openAPIHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	apiDomainKey := dyncamiccontext.APIDomainKeyFrom(ctx)

	// Get all available APIDefinitions for this Virtual Workspace
	apiSet, hasLocationKey, err := o.apiSetRetriever.GetAPIDefinitionSet(ctx, apiDomainKey)
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("unable to determine API definition set: %w", err)),
			errorCodecs, schema.GroupVersion{},
			w, req)
		return
	}
	if !hasLocationKey {
		o.delegate.ServeHTTP(w, req)
		return
	}

	// TODO(xmudrii): Add ordering here.
	// TODO(xmudrii): Add caching here.

	// Collect routes registered in the virtual workspace API server: /api, /apis, and /version.
	// Subroutes (e.g. /apis/<group> and /apis/<group>/<version>) are not included and are handled separately below.
	webservices := make(map[string][]*restful.WebService)
	for _, t := range o.goRestfulContainer.RegisteredWebServices() {
		// Strip the "/" prefix from the name
		gvPath := t.RootPath()[1:]
		webservices[gvPath] = []*restful.WebService{t}
	}

	// Create web services for /apis/<group>
	versionsForDiscoveryMap := make(map[schema.GroupVersion][]metav1.GroupVersionForDiscovery)
	for gvr := range apiSet {
		if gvr.Group == "" {
			continue
		}
		gv := schema.GroupVersion{
			Group:   gvr.Group,
			Version: gvr.Version,
		}

		if versionsForDiscoveryMap[gv] == nil {
			versionsForDiscoveryMap[gv] = []metav1.GroupVersionForDiscovery{}
		}

		versionsForDiscoveryMap[gv] = append(versionsForDiscoveryMap[gv], metav1.GroupVersionForDiscovery{
			GroupVersion: gvr.GroupVersion().String(),
			Version:      gvr.Version,
		})
	}
	for gv := range versionsForDiscoveryMap {
		apiGroup := metav1.APIGroup{
			Name:     gv.Group,
			Versions: versionsForDiscoveryMap[gv],
			// the preferred versions for a group is the first item in
			// apiVersionsForDiscovery after it put in the right ordered
			PreferredVersion: versionsForDiscoveryMap[gv][0],
		}

		groupPath := "apis/" + gv.Group
		webservices[groupPath] = append(webservices[groupPath], discovery.NewAPIGroupHandler(codecs, apiGroup).WebService())
	}

	// Build OpenAPI v3 spec for /apis/<group> webservices
	routeSpecs := make(map[string][]*spec3.OpenAPI)
	for groupPath, ws := range webservices {
		spec, err := builder3.BuildOpenAPISpecFromRoutes(restfuladapter.AdaptWebServices(ws), o.openapiv3Config)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}
		routeSpecs[groupPath] = []*spec3.OpenAPI{spec}
	}

	// Build OpenAPI v3 specs for /apis/<group>/<version>, i.e. build OpenAPI v3 specs for each APIDefinition
	resourceSpecs := make([]map[string][]*spec3.OpenAPI, 0, len(apiSet))
	for _, apiDefinition := range apiSet {
		specs, err := apiResourceSchemaToSpec(apiDefinition.GetAPIResourceSchema())
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}
		resourceSpecs = append(resourceSpecs, specs)
	}

	// Create a new OpenAPI service
	m := mux.NewPathRecorderMux("virtual-workspace-openapi-v3")
	service := handler3.NewOpenAPIService()
	err = service.RegisterOpenAPIV3VersionedService("/openapi/v3", m)
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return
	}

	// Add all OpenAPI v3 specs to the OpenAPI service
	err = addSpecs(service, routeSpecs, resourceSpecs)
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return
	}

	m.ServeHTTP(w, req)
}

func apiResourceSchemaToSpec(apiResourceSchema *apisv1alpha1.APIResourceSchema) (map[string][]*spec3.OpenAPI, error) {
	specs := map[string][]*spec3.OpenAPI{}
	versions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(apiResourceSchema.Spec.Versions))

	for _, ver := range apiResourceSchema.Spec.Versions {
		verSchema, err := ver.GetSchema()
		if err != nil {
			return nil, err
		}

		versions = append(versions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name: ver.Name,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: verSchema,
			},
			Subresources: &ver.Subresources,
		})
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    apiResourceSchema.Spec.Group,
			Names:    apiResourceSchema.Spec.Names,
			Versions: versions,
			Scope:    apiResourceSchema.Spec.Scope,
		},
	}

	for _, ver := range versions {
		spec, err := builder.BuildOpenAPIV3(crd, ver.Name, builder.Options{V2: false})
		if err != nil {
			return nil, err
		}

		gv := schema.GroupVersion{Group: crd.Spec.Group, Version: ver.Name}
		gvPath := groupVersionToOpenAPIV3Path(gv)
		specs[gvPath] = append(specs[gvPath], spec)
	}

	return specs, nil
}

func groupVersionToOpenAPIV3Path(gv schema.GroupVersion) string {
	if gv.Group == "" {
		return "api/" + gv.Version
	}
	return "apis/" + gv.Group + "/" + gv.Version
}

func addSpecs(service *handler3.OpenAPIService, routeSpecs map[string][]*spec3.OpenAPI, apiDefSpecs []map[string][]*spec3.OpenAPI) error {
	byGroupVersionSpecs := make(map[string][]*spec3.OpenAPI)

	for groupPath, spec := range routeSpecs {
		byGroupVersionSpecs[groupPath] = spec
	}

	for _, apiDefSpec := range apiDefSpecs {
		for gvPath, spec := range apiDefSpec {
			byGroupVersionSpecs[gvPath] = spec
		}
	}

	// lazily merge spec and add to service
	for gvPath, specs := range byGroupVersionSpecs {
		gvSpec, err := builder.MergeSpecsV3(specs...)
		if err != nil {
			return fmt.Errorf("failed to merge specs: %v", err)
		}

		service.UpdateGroupVersion(gvPath, gvSpec)
	}

	return nil
}
