/*
Copyright 2022 The kcp Authors.

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
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/kubernetes/pkg/controlplane/apiserver/miniaggregator"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dyncamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

type versionDiscoveryHandler struct {
	apiSetRetriever apidefinition.APIDefinitionSetGetter
	delegate        http.Handler
}

func (r *versionDiscoveryHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	pathParts := splitPath(req.URL.Path)
	var requestedGroup string
	var requestedVersion string

	// only match /apis/<group>/<version> or /api/<version>
	if len(pathParts) == 3 && pathParts[0] == "apis" {
		requestedGroup = pathParts[1]
		requestedVersion = pathParts[2]
	} else if len(pathParts) == 2 && pathParts[0] == "api" {
		requestedGroup = ""
		requestedVersion = pathParts[1]
	} else {
		r.delegate.ServeHTTP(w, req)
		return
	}

	ctx := req.Context()

	apiDomainKey := dyncamiccontext.APIDomainKeyFrom(ctx)

	apiSet, hasLocationKey, err := r.apiSetRetriever.GetAPIDefinitionSet(ctx, apiDomainKey)
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

	apiResourcesForDiscovery := []metav1.APIResource{}

	foundGroupVersion := false

	for gvr, apiDef := range apiSet {
		if requestedGroup != gvr.Group || requestedVersion != gvr.Version {
			continue
		}
		foundGroupVersion = true

		apiResourceSchema := apiDef.GetAPIResourceSchema()
		storageVersionHash := discovery.StorageVersionHash(apiDef.GetClusterName(), gvr.Group, gvr.Version, apiResourceSchema.Spec.Names.Kind)
		apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
			Name:               apiResourceSchema.Spec.Names.Plural,
			SingularName:       apiResourceSchema.Spec.Names.Singular,
			Namespaced:         apiResourceSchema.Spec.Scope == apiextensionsv1.NamespaceScoped,
			Kind:               apiResourceSchema.Spec.Names.Kind,
			Verbs:              supportedVerbs(apiDef.GetStorage()),
			ShortNames:         apiResourceSchema.Spec.Names.ShortNames,
			Categories:         apiResourceSchema.Spec.Names.Categories,
			StorageVersionHash: storageVersionHash,
		})

		// Advertise every sub-resource that was actually built, rather than only the
		// ones the APIResourceSchema can name. status and scale come from the schema;
		// custom sub-resources come from the APIExport and reach us only as entries in
		// the storage map, so the map is the authoritative list.
		//
		// The kind is taken from the storage's own object when it has one, because a
		// sub-resource may produce a different kind from its parent (scale produces
		// autoscaling/v1 Scale, and a custom sub-resource may produce anything).
		for _, subresource := range apiDef.GetSubResourceNames() {
			storage := apiDef.GetSubResourceStorage(subresource)
			if storage == nil {
				continue
			}

			apiResource := metav1.APIResource{
				Name:       apiResourceSchema.Spec.Names.Plural + "/" + subresource,
				Namespaced: apiResourceSchema.Spec.Scope == apiextensionsv1.NamespaceScoped,
				Kind:       apiResourceSchema.Spec.Names.Kind,
				Verbs:      supportedVerbs(storage),
			}

			if obj := storage.New(); obj != nil {
				if gvk := obj.GetObjectKind().GroupVersionKind(); !gvk.Empty() && gvk.Kind != apiResourceSchema.Spec.Names.Kind {
					apiResource.Group = gvk.Group
					apiResource.Version = gvk.Version
					apiResource.Kind = gvk.Kind
				}
			}

			apiResourcesForDiscovery = append(apiResourcesForDiscovery, apiResource)
		}
	}

	resourceListerFunc := discovery.APIResourceListerFunc(func() []metav1.APIResource {
		return apiResourcesForDiscovery
	})

	if !foundGroupVersion {
		r.delegate.ServeHTTP(w, req)
		return
	}

	discovery.NewAPIVersionHandler(codecs, schema.GroupVersion{Group: requestedGroup, Version: requestedVersion}, resourceListerFunc).ServeHTTP(w, req)
}

// connectVerbs reports the verbs a Connecter is reachable by. A connect subresource
// is authorized under the verb derived from the HTTP method, not under a literal
// "connect": kubectl exec POSTs to pods/exec and therefore needs "create" on it. The
// methods come from the storage so that discovery cannot disagree with what the
// handler will actually accept.
func connectVerbs(connecter rest.Connecter) metav1.Verbs {
	byMethod := map[string]string{
		"GET":    "get",
		"POST":   "create",
		"PUT":    "update",
		"PATCH":  "patch",
		"DELETE": "delete",
	}

	seen := sets.New[string]()
	for _, method := range connecter.ConnectMethods() {
		if verb, ok := byMethod[strings.ToUpper(method)]; ok {
			seen.Insert(verb)
		}
	}

	return metav1.Verbs(sets.List(seen)) // sets.List sorts, keeping discovery stable
}

func supportedVerbs(storage rest.Storage) metav1.Verbs {
	// A Connecter is not a Getter or a Creater, so the type assertions below would
	// report no verbs at all for it. Its methods are the authoritative answer.
	if connecter, isAble := storage.(rest.Connecter); isAble {
		return connectVerbs(connecter)
	}

	var verbs metav1.Verbs // given the below order, these will always be in lexicographical order
	if _, canCreate := storage.(rest.Creater); canCreate {
		verbs = append(verbs, "create")
	}
	if _, canDelete := storage.(rest.GracefulDeleter); canDelete {
		verbs = append(verbs, "delete")
	}
	if _, canDeleteCollections := storage.(rest.CollectionDeleter); canDeleteCollections {
		verbs = append(verbs, "deletecollection")
	}
	if _, canGet := storage.(rest.Getter); canGet {
		verbs = append(verbs, "get")
	}
	if _, canList := storage.(rest.Lister); canList {
		verbs = append(verbs, "list")
	}
	if _, canPatch := storage.(rest.Patcher); canPatch {
		verbs = append(verbs, "patch")
	}
	if _, canUpdate := storage.(rest.Updater); canUpdate {
		verbs = append(verbs, "update")
	}
	if _, canWatch := storage.(rest.Watcher); canWatch {
		verbs = append(verbs, "watch")
	}
	return verbs
}

type groupDiscoveryHandler struct {
	apiSetRetriever apidefinition.APIDefinitionSetGetter
	delegate        http.Handler
}

func (r *groupDiscoveryHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	pathParts := splitPath(req.URL.Path)
	// only match /apis/<group>

	var requestedGroup string
	if len(pathParts) == 2 && pathParts[0] == "apis" {
		requestedGroup = pathParts[1]
	} else if len(pathParts) == 1 && pathParts[0] == "api" {
		requestedGroup = ""
	} else {
		r.delegate.ServeHTTP(w, req)
		return
	}

	apiVersionsForDiscovery := []metav1.GroupVersionForDiscovery{}
	versionsForDiscoveryMap := map[metav1.GroupVersion]bool{}

	ctx := req.Context()

	apiDomainKey := dyncamiccontext.APIDomainKeyFrom(ctx)

	apiSet, hasLocationKey, err := r.apiSetRetriever.GetAPIDefinitionSet(ctx, apiDomainKey)
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

	foundGroup := false

	for gvr := range apiSet {
		if requestedGroup != gvr.Group {
			continue
		}

		foundGroup = true

		gv := metav1.GroupVersion{
			Group:   gvr.Group,
			Version: gvr.Version,
		}

		if !versionsForDiscoveryMap[gv] {
			versionsForDiscoveryMap[gv] = true
			apiVersionsForDiscovery = append(apiVersionsForDiscovery, metav1.GroupVersionForDiscovery{
				GroupVersion: gvr.GroupVersion().String(),
				Version:      gvr.Version,
			})
		}
	}

	sortGroupDiscoveryByKubeAwareVersion(apiVersionsForDiscovery)

	if !foundGroup {
		r.delegate.ServeHTTP(w, req)
		return
	}

	apiGroup := metav1.APIGroup{
		Name:     requestedGroup,
		Versions: apiVersionsForDiscovery,
		// the preferred versions for a group is the first item in
		// apiVersionsForDiscovery after it put in the right ordered
		PreferredVersion: apiVersionsForDiscovery[0],
	}

	discovery.NewAPIGroupHandler(codecs, apiGroup).ServeHTTP(w, req)
}

type rootDiscoveryHandler struct {
	apiSetRetriever apidefinition.APIDefinitionSetGetter
	delegate        http.Handler
}

func (r *rootDiscoveryHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	apiVersionsForDiscovery := map[string][]metav1.GroupVersionForDiscovery{}
	versionsForDiscoveryMap := map[string]map[metav1.GroupVersion]bool{}

	ctx := req.Context()
	apiDomainKey := dyncamiccontext.APIDomainKeyFrom(ctx)

	apiSet, hasLocationKey, err := r.apiSetRetriever.GetAPIDefinitionSet(ctx, apiDomainKey)
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

	for gvr := range apiSet {
		if gvr.Group == "" {
			// Don't include CRDs in the core ("") group in /apis discovery. They
			// instead are in /api/v1 handled elsewhere.
			continue
		}
		groupVersion := gvr.GroupVersion().String()

		gv := metav1.GroupVersion{Group: gvr.Group, Version: gvr.Version}

		m, ok := versionsForDiscoveryMap[gvr.Group]
		if !ok {
			m = make(map[metav1.GroupVersion]bool)
		}

		if !m[gv] {
			m[gv] = true
			groupVersions := apiVersionsForDiscovery[gvr.Group]
			groupVersions = append(groupVersions, metav1.GroupVersionForDiscovery{
				GroupVersion: groupVersion,
				Version:      gvr.Version,
			})
			apiVersionsForDiscovery[gvr.Group] = groupVersions
		}

		versionsForDiscoveryMap[gvr.Group] = m
	}

	for _, versions := range apiVersionsForDiscovery {
		sortGroupDiscoveryByKubeAwareVersion(versions)
	}

	groupList := make([]metav1.APIGroup, 0, len(apiVersionsForDiscovery))
	for group, versions := range apiVersionsForDiscovery {
		g := metav1.APIGroup{
			Name:             group,
			Versions:         versions,
			PreferredVersion: versions[0],
		}
		groupList = append(groupList, g)
	}
	responsewriters.WriteObjectNegotiated(miniaggregator.DiscoveryCodecs, negotiation.DefaultEndpointRestrictions, schema.GroupVersion{}, w, req, http.StatusOK, &metav1.APIGroupList{Groups: groupList}, false)
}

// splitPath returns the segments for a URL path.
func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}

func sortGroupDiscoveryByKubeAwareVersion(gd []metav1.GroupVersionForDiscovery) {
	sort.Slice(gd, func(i, j int) bool {
		return version.CompareKubeAwareVersionStrings(gd[i].Version, gd[j].Version) > 0
	})
}
