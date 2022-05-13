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
	"net/http"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/kubernetes/pkg/genericcontrolplane/aggregator"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dyncamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

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

	apiSet, hasLocationKey := r.apiSetRetriever.GetAPIDefinitionSet(apiDomainKey)
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

		var (
			storageVersionHash string
			subresources       apiresourcev1alpha1.SubResources
		)

		apiResourceSpec := apiDef.GetAPIResourceSpec()
		subresources = apiResourceSpec.SubResources

		// TODO: get the list of verbs from the REST storage instance
		verbs := metav1.Verbs([]string{"get", "list", "patch", "create", "update", "watch"})

		storageVersionHash = discovery.StorageVersionHash(apiDef.GetClusterName().String(), gvr.Group, gvr.Version, apiDef.GetAPIResourceSpec().Kind)

		apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
			Name:               apiResourceSpec.Plural,
			SingularName:       apiResourceSpec.Singular,
			Namespaced:         apiResourceSpec.Scope == apiextensionsv1.NamespaceScoped,
			Kind:               apiResourceSpec.Kind,
			Verbs:              verbs,
			ShortNames:         apiResourceSpec.ShortNames,
			Categories:         apiResourceSpec.Categories,
			StorageVersionHash: storageVersionHash,
		})

		if subresources != nil && subresources.Contains("status") {
			// TODO: get the list of verbs from the StatusREST storage instance
			apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
				Name:       apiResourceSpec.Plural + "/status",
				Namespaced: apiResourceSpec.Scope == apiextensionsv1.NamespaceScoped,
				Kind:       apiResourceSpec.Kind,
				Verbs:      metav1.Verbs([]string{"get", "patch", "update"}),
			})
		}

		// TODO(david): Add scale sub-resource ???
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

	apiSet, hasLocationKey := r.apiSetRetriever.GetAPIDefinitionSet(apiDomainKey)
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

	apiSet, hasLocationKey := r.apiSetRetriever.GetAPIDefinitionSet(apiDomainKey)
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
	responsewriters.WriteObjectNegotiated(aggregator.DiscoveryCodecs, negotiation.DefaultEndpointRestrictions, schema.GroupVersion{}, w, req, http.StatusOK, &metav1.APIGroupList{Groups: groupList})
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
