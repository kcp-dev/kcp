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

package apiserver

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emicklei/go-restful/v3"
	"github.com/go-logr/logr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/builder3"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/common/restfuladapter"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/utils/keymutex"
	"k8s.io/utils/lru"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dyncamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

const (
	// DefaultServiceCacheSize is the default size of the OpenAPI service cache.
	DefaultServiceCacheSize = 50
)

type openAPIServiceItem struct {
	done    <-chan struct{}
	err     error
	service http.Handler
}

// openAPIHandler implements the OpenAPI v3 handler for virtual workspaces.
//
// This handler generates the OpenAPI v3 spec by:
//
//   - Creating specs based on webservices registered with the virtual workspace API server. These are only /api, /apis
//     and /version, without any subroute (e.g. /apis/<group>)
//   - Creating a webservice for every /apis/<group> route available by utilizing the API discovery, then generating
//     OpenAPI v3 specs from these webservices
//   - Converting every APIDefinition into a CustomResourceDefinition and generating OpenAPI v3 specs for
//     /apis/<group>/<version> from these CRDs using the kube-openapi library
//
// This is different to the regular API servers, which are generating the OpenAPI v3 spec by purely using the
// webservices. This is not an option here because the virtual workspace API server has only three webservices
// registered (/api, /apis, and /version, without any subroute).
type openAPIHandler struct {
	apiSetRetriever    apidefinition.APIDefinitionSetGetter
	goRestfulContainer *restful.Container
	openapiv3Config    *common.OpenAPIV3Config
	delegate           http.Handler

	services         *lru.Cache
	servicesLock     keymutex.KeyMutex
	openAPIV3Service *handler3.OpenAPIService
}

func newOpenAPIHandler(
	apiSetRetriever apidefinition.APIDefinitionSetGetter,
	goRestfulContainer *restful.Container,
	openapiv3Config *common.OpenAPIV3Config,
	serviceCacheSize int,
	delegate http.Handler) *openAPIHandler {
	openAPIV3Service := handler3.NewOpenAPIService()

	return &openAPIHandler{
		apiSetRetriever:    apiSetRetriever,
		goRestfulContainer: goRestfulContainer,
		openapiv3Config:    openapiv3Config,
		delegate:           delegate,

		services:         lru.New(serviceCacheSize),
		servicesLock:     keymutex.NewHashed(0),
		openAPIV3Service: openAPIV3Service,
	}
}

func (o *openAPIHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	log := klog.FromContext(ctx).WithValues("path", req.URL.Path)
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

	// Get the OpenAPI service from cache or create a new one
	key, err := apiConfigurationKey(apiSet)
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return
	}
	log = log.WithValues("key", key)

	m, err := o.handleOpenAPIRequest(apiSet, key, log)
	if err != nil {
		responsewriters.InternalError(w, req, err)
		return
	}

	m.ServeHTTP(w, req)
}

func (o *openAPIHandler) handleOpenAPIRequest(apiSet apidefinition.APIDefinitionSet, key string, log logr.Logger) (http.Handler, error) {
	// We want to make sure we don't generate OpenAPI v3 spec if not necessary because it's an expensive operation.
	// Putting the generated OpenAPI v3 service in a cache using the configuration key generated above solves this in
	// the most of cases.
	// However, it doesn't cover the case where we have two concurrent requests with the same caching key, in which
	// we would generate the OpenAPI v3 spec two times for the same key (if the spec is not cached already).
	// That's why we have a "double-locking mechanism":
	//   1) we store a special struct in a cache that contains a channel and an OpenAPI v3 service; if the channel is
	//      closed, we know that we generated the OpenAPI v3 spec for that key and we can just serve it
	// 	 2) that struct is supposed to be a singleton for a given caching key, and because of that, we need some
	//      guarantee that no two threads are going to try to create an instance of that struct for the same key
	//      (that's why we have servicesLock)
	o.servicesLock.LockKey(key)
	entry, ok := o.services.Get(key)
	var item *openAPIServiceItem

	// If we found the struct, and the channel is closed, and the spec was generated successfully, serve the already
	// generated spec, otherwise regenerate it.
	if ok {
		cached := entry.(*openAPIServiceItem)
		// Wait for the previous attempt outside the key lock so concurrent
		// requests can proceed; we still hold the key lock for the cache miss
		// path below, so only one regeneration runs at a time per key.
		_ = o.servicesLock.UnlockKey(key) // error is always nil
		<-cached.done
		if cached.err == nil {
			log.V(7).Info("Reusing OpenAPI v3 service from cache")
			return cached.service, nil
		}
		// Cached entry recorded an error — reacquire the key lock so we can
		// regenerate. Without this we'd fall through and call UnlockKey again
		// on an unlocked mutex, panicking sync.Mutex.
		o.servicesLock.LockKey(key)
	}

	doneCh := make(chan struct{})
	item = &openAPIServiceItem{
		done: doneCh,
	}
	o.services.Add(key, item)
	_ = o.servicesLock.UnlockKey(key) // error is always nil

	start := time.Now()
	defer func() {
		close(doneCh)
		elapsed := time.Since(start)
		if elapsed > time.Second {
			log.Info("slow openapi v3 aggregation", "duration", elapsed)
		}
	}()

	// If we got here, we have nothing in cache or cache is invalid, so we just generate everything again and serve it.
	log.V(7).Info("Generating OpenAPI v3 specs")

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
		if _, ok := webservices[groupPath]; !ok {
			webservices[groupPath] = append(webservices[groupPath], discovery.NewAPIGroupHandler(codecs, apiGroup).WebService())
		}
	}

	// Build OpenAPI v3 spec for /apis/<group> webservices
	routeSpecs := make(map[string][]*spec3.OpenAPI)
	for groupPath, ws := range webservices {
		spec, err := builder3.BuildOpenAPISpecFromRoutes(restfuladapter.AdaptWebServices(ws), o.openapiv3Config)
		if err != nil {
			item.err = err
			return nil, err
		}
		routeSpecs[groupPath] = []*spec3.OpenAPI{spec}
	}

	// Build OpenAPI v3 specs for /apis/<group>/<version>, i.e. build OpenAPI v3 specs for each APIDefinition.
	// We pass the GVR's version so a multi-version APIResourceSchema (e.g. built-in apibindings carrying
	// both v1alpha1 and v1alpha2) emits a spec only for the version this apiSet entry is keyed on.
	// Otherwise the same APIResourceSchema referenced from two apiSet entries (one per version) would
	// yield duplicate specs that addSpecs then tries to merge, which trips the strict component-name
	// conflict check.
	resourceSpecs := make([]map[string][]*spec3.OpenAPI, 0, len(apiSet))
	for gvr, apiDefinition := range apiSet {
		specs, err := apiResourceSchemaToSpec(apiDefinition.GetAPIResourceSchema(), gvr.Version)
		if err != nil {
			item.err = err
			return nil, err
		}
		resourceSpecs = append(resourceSpecs, specs)
	}

	// Create a new OpenAPI service
	log.V(7).Info("Creating new OpenAPI v3 service")
	m := mux.NewPathRecorderMux("virtual-workspace-openapi-v3")
	service := handler3.NewOpenAPIService()
	err := service.RegisterOpenAPIV3VersionedService("/openapi/v3", m)
	if err != nil {
		item.err = err
		return nil, err
	}

	// Add all OpenAPI v3 specs to the OpenAPI service
	err = addSpecs(service, routeSpecs, resourceSpecs, log)
	if err != nil {
		item.err = err
		return nil, err
	}

	item.service = m
	return item.service, nil
}

// apiResourceSchemaToSpec builds the OpenAPI v3 spec for the given APIResourceSchema.
// If onlyVersion is non-empty, only that single version is emitted; this lets callers
// avoid duplicate specs when the same multi-version APIResourceSchema is reached via
// multiple per-version apiSet entries.
func apiResourceSchemaToSpec(apiResourceSchema *apisv1alpha1.APIResourceSchema, onlyVersion string) (map[string][]*spec3.OpenAPI, error) {
	specs := map[string][]*spec3.OpenAPI{}
	versions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(apiResourceSchema.Spec.Versions))
	srcVersions := make([]apisv1alpha1.APIResourceVersion, 0, len(apiResourceSchema.Spec.Versions))

	for _, ver := range apiResourceSchema.Spec.Versions {
		if onlyVersion != "" && ver.Name != onlyVersion {
			continue
		}
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
		srcVersions = append(srcVersions, ver)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group:    apiResourceSchema.Spec.Group,
			Names:    apiResourceSchema.Spec.Names,
			Versions: versions,
			Scope:    apiResourceSchema.Spec.Scope,
		},
	}

	for i, ver := range versions {
		spec, err := builder.BuildOpenAPIV3(crd, ver.Name, builder.Options{V2: false})
		if err != nil {
			return nil, err
		}

		if isCRDResource(crd) {
			patchCRDSchemaForOpenAPI(spec, &srcVersions[i])
		}

		gv := schema.GroupVersion{Group: crd.Spec.Group, Version: ver.Name}
		gvPath := groupVersionToOpenAPIV3Path(gv)
		specs[gvPath] = append(specs[gvPath], spec)
	}

	return specs, nil
}

func patchCRDSchemaForOpenAPI(spec *spec3.OpenAPI, ver *apisv1alpha1.APIResourceVersion) {
	if spec.Components == nil || spec.Components.Schemas == nil {
		return
	}

	for name, s := range spec.Components.Schemas {
		if !strings.HasSuffix(name, "CustomResourceDefinition") {
			continue
		}
		if _, hasSpec := s.Properties["spec"]; hasSpec {
			continue
		}

		sourceSchema, err := ver.GetSchema()
		if err != nil || sourceSchema == nil {
			continue
		}

		patchedSchema, err := convertToOpenAPISchema(sourceSchema)
		if err != nil {
			continue
		}

		patchedSchema.VendorExtensible.Extensions = s.VendorExtensible.Extensions
		enhanceOpenAPIV3SchemaProperty(&patchedSchema)
		spec.Components.Schemas[name] = &patchedSchema
	}
}

func convertToOpenAPISchema(source *apiextensionsv1.JSONSchemaProps) (spec.Schema, error) {
	bs, err := json.Marshal(source)
	if err != nil {
		return spec.Schema{}, err
	}

	var result spec.Schema
	if err := json.Unmarshal(bs, &result); err != nil {
		return spec.Schema{}, err
	}

	return result, nil
}

func enhanceOpenAPIV3SchemaProperty(schema *spec.Schema) {
	specProp, ok := schema.Properties["spec"]
	if !ok {
		return
	}

	versionsProp, ok := specProp.Properties["versions"]
	if !ok {
		return
	}

	if versionsProp.Items == nil || versionsProp.Items.Schema == nil {
		return
	}

	itemSchema := versionsProp.Items.Schema
	schemaProp, ok := itemSchema.Properties["schema"]
	if !ok {
		return
	}

	oaProp, ok := schemaProp.Properties["openAPIV3Schema"]
	if !ok {
		return
	}

	// We apply x-kubernetes-preserve-unknown-fields so OpenAPI consumers (like kubectl)
	// know that openAPIV3Schema can contain arbitrary objects, without requiring us to
	// recursively unroll the massively nested JSONSchemaProps type which exhausts the API server.
	if oaProp.VendorExtensible.Extensions == nil {
		oaProp.VendorExtensible.Extensions = spec.Extensions{}
	}
	oaProp.VendorExtensible.Extensions["x-kubernetes-preserve-unknown-fields"] = true

	oaProp.Properties = nil
	oaProp.Type = []string{"object"}

	schemaProp.Properties["openAPIV3Schema"] = oaProp
	itemSchema.Properties["schema"] = schemaProp
	versionsProp.Items.Schema = itemSchema
	specProp.Properties["versions"] = versionsProp
	schema.Properties["spec"] = specProp
}

func groupVersionToOpenAPIV3Path(gv schema.GroupVersion) string {
	if gv.Group == "" {
		return "api/" + gv.Version
	}
	return "apis/" + gv.Group + "/" + gv.Version
}

func addSpecs(service *handler3.OpenAPIService, routeSpecs map[string][]*spec3.OpenAPI, apiDefSpecs []map[string][]*spec3.OpenAPI, log logr.Logger) error {
	byGroupVersionSpecs := make(map[string][]*spec3.OpenAPI)

	for groupPath, spec := range routeSpecs {
		byGroupVersionSpecs[groupPath] = spec
	}

	for _, apiDefSpec := range apiDefSpecs {
		for gvPath, spec := range apiDefSpec {
			// Append rather than assign: multiple APIDefinitions may produce
			// specs for the same group/version (e.g. several Kinds bound under
			// one GV via an APIExport). MergeSpecsV3 below merges the slice.
			byGroupVersionSpecs[gvPath] = append(byGroupVersionSpecs[gvPath], spec...)
		}
	}

	// lazily merge spec and add to service
	for gvPath, specs := range byGroupVersionSpecs {
		log.V(6).Info("Merging OpenAPI v3 specs", "gvPath", gvPath)

		gvSpec, err := mergeSpecsV3Tolerant(specs)
		if err != nil {
			return fmt.Errorf("failed to merge specs: %v", err)
		}

		service.UpdateGroupVersion(gvPath, gvSpec)
	}

	return nil
}

// mergeSpecsV3Tolerant merges OpenAPI v3 specs that share a group/version.
//
// Unlike apiextensions-apiserver's builder.MergeSpecsV3 it does NOT error on
// shared component-schema names: when several CRDs are bound under the same
// group/version (the case this handler is built for), every CRD's spec
// re-emits common helper types (meta.v1.*, autoscaling.v1.*, and kcp-specific
// types like apis.v1alpha1.APIBinding). The strict merge treats those as
// conflicts. Here we keep the first definition we see — they're semantically
// the same regardless of which CRD emitted them.
//
// Paths from later specs override earlier ones with the same key, which is
// the natural behavior: each path lives on exactly one resource.
func mergeSpecsV3Tolerant(specs []*spec3.OpenAPI) (*spec3.OpenAPI, error) {
	merged := &spec3.OpenAPI{}
	if len(specs) > 0 {
		merged.Version = specs[0].Version
		merged.Info = specs[0].Info
	}
	for _, s := range specs {
		if s == nil {
			continue
		}
		if s.Components != nil {
			if merged.Components == nil {
				merged.Components = &spec3.Components{}
			}
			if merged.Components.Schemas == nil {
				merged.Components.Schemas = map[string]*spec.Schema{}
			}
			for k, v := range s.Components.Schemas {
				if _, exists := merged.Components.Schemas[k]; exists {
					// keep first; common helper types may differ slightly
					// across per-CRD specs (e.g. description ordering) but
					// are semantically equivalent.
					continue
				}
				merged.Components.Schemas[k] = v
			}
		}
		if s.Paths != nil {
			if merged.Paths == nil {
				merged.Paths = &spec3.Paths{}
			}
			if merged.Paths.Paths == nil {
				merged.Paths.Paths = map[string]*spec3.Path{}
			}
			for k, v := range s.Paths.Paths {
				merged.Paths.Paths[k] = v
			}
		}
	}
	return merged, nil
}

func apiConfigurationKey(apiDefs apidefinition.APIDefinitionSet) (string, error) {
	buf := &bytes.Buffer{}

	keys := make([]schema.GroupVersionResource, 0, len(apiDefs))
	for k := range apiDefs {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})

	firstAPIDef := true
	for _, k := range keys {
		apiDefSchema := apiDefs[k].GetAPIResourceSchema()
		bs, err := json.Marshal(apiDefSchema.Spec)
		if err != nil {
			return "", fmt.Errorf("failed to marshal APIDefinition spec: %w", err)
		}

		if !firstAPIDef {
			buf.WriteByte(';')
		}

		buf.WriteString(apiDefSchema.Name)
		buf.WriteByte(':')
		fmt.Fprintf(buf, "%X", sha512.Sum512(bs))

		firstAPIDef = false
	}

	return buf.String(), nil
}
