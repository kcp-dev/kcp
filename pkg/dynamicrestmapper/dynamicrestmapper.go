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

package dynamicrestmapper

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type DynamicRESTMapper struct {
	defaultGroupVersions []schema.GroupVersion

	resourceToKind       map[schema.GroupVersionResource]schema.GroupVersionKind
	kindToPluralResource map[schema.GroupVersionKind]schema.GroupVersionResource
	kindToScope          map[schema.GroupVersionKind]meta.RESTScope
	singularToPlural     map[schema.GroupVersionResource]schema.GroupVersionResource
	pluralToSingular     map[schema.GroupVersionResource]schema.GroupVersionResource

	// Protects all DynamicRESTMapper's mappings.
	mapperLock sync.RWMutex
}

var _ meta.RESTMapper = &DynamicRESTMapper{}

func NewDynamicRESTMapper(defaultGroupVersions []schema.GroupVersion) *DynamicRESTMapper {
	resourceToKind := make(map[schema.GroupVersionResource]schema.GroupVersionKind)
	kindToPluralResource := make(map[schema.GroupVersionKind]schema.GroupVersionResource)
	kindToScope := make(map[schema.GroupVersionKind]meta.RESTScope)
	singularToPlural := make(map[schema.GroupVersionResource]schema.GroupVersionResource)
	pluralToSingular := make(map[schema.GroupVersionResource]schema.GroupVersionResource)
	// TODO: verify name mappings work correctly when versions differ

	return &DynamicRESTMapper{
		resourceToKind:       resourceToKind,
		kindToPluralResource: kindToPluralResource,
		kindToScope:          kindToScope,
		defaultGroupVersions: defaultGroupVersions,
		singularToPlural:     singularToPlural,
		pluralToSingular:     pluralToSingular,
	}
}

func (m *DynamicRESTMapper) String() string {
	if m == nil {
		return "<nil>"
	}
	return fmt.Sprintf("DynamicRESTMapper{kindToPluralResource=%v}", m.kindToPluralResource)
}

func (m *DynamicRESTMapper) add(typeMeta gvkr, scope meta.RESTScope) {
	var (
		kind     = typeMeta.groupVersionKind()
		singular schema.GroupVersionResource
		plural   schema.GroupVersionResource
	)

	if typeMeta.ResourceSingular == "" || typeMeta.ResourcePlural == "" {
		singular, singular = meta.UnsafeGuessKindToResource(kind)
	} else {
		singular = typeMeta.groupVersionResourceSingular()
		plural = typeMeta.groupVersionResourcePlural()
	}

	m.mapperLock.Lock()
	defer m.mapperLock.Unlock()

	m.singularToPlural[singular] = plural
	m.pluralToSingular[plural] = singular

	m.resourceToKind[singular] = kind
	m.resourceToKind[plural] = kind

	m.kindToPluralResource[kind] = plural
	m.kindToScope[kind] = scope
}

func (m *DynamicRESTMapper) remove(gvk schema.GroupVersionKind) {
	m.mapperLock.Lock()
	defer m.mapperLock.Unlock()

	singular := m.kindToPluralResource[gvk]
	plural := m.singularToPlural[singular]

	delete(m.kindToPluralResource, gvk)
	delete(m.kindToScope, gvk)
	delete(m.singularToPlural, singular)
	delete(m.pluralToSingular, plural)
	delete(m.resourceToKind, plural)
}

// KindFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches
func (m *DynamicRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	kinds, err := m.KindsFor(resource)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(kinds) == 1 {
		return kinds[0], nil
	}

	return schema.GroupVersionKind{}, &meta.AmbiguousResourceError{PartialResource: resource, MatchingKinds: kinds}
}

type kindByPreferredGroupVersion struct {
	list      []schema.GroupVersionKind
	sortOrder []schema.GroupVersion
}

func (o kindByPreferredGroupVersion) Len() int      { return len(o.list) }
func (o kindByPreferredGroupVersion) Swap(i, j int) { o.list[i], o.list[j] = o.list[j], o.list[i] }
func (o kindByPreferredGroupVersion) Less(i, j int) bool {
	lhs := o.list[i]
	rhs := o.list[j]
	if lhs == rhs {
		return false
	}

	if lhs.GroupVersion() == rhs.GroupVersion() {
		return lhs.Kind < rhs.Kind
	}

	// otherwise, the difference is in the GroupVersion, so we need to sort with respect to the preferred order
	lhsIndex := -1
	rhsIndex := -1

	for i := range o.sortOrder {
		if o.sortOrder[i] == lhs.GroupVersion() {
			lhsIndex = i
		}
		if o.sortOrder[i] == rhs.GroupVersion() {
			rhsIndex = i
		}
	}

	if rhsIndex == -1 {
		return true
	}

	return lhsIndex < rhsIndex
}

// KindsFor takes a partial resource and returns the list of potential kinds in priority order
func (m *DynamicRESTMapper) KindsFor(input schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	resource := coerceResourceForMatching(input)

	hasResource := len(resource.Resource) > 0
	hasGroup := len(resource.Group) > 0
	hasVersion := len(resource.Version) > 0

	if !hasResource {
		return nil, fmt.Errorf("a resource must be present, got: %v", resource)
	}

	ret := []schema.GroupVersionKind{}
	switch {
	// fully qualified.  Find the exact match
	case hasGroup && hasVersion:
		kind, exists := m.resourceToKind[resource]
		if exists {
			ret = append(ret, kind)
		}

	case hasGroup:
		foundExactMatch := false
		requestedGroupResource := resource.GroupResource()
		for currResource, currKind := range m.resourceToKind {
			if currResource.GroupResource() == requestedGroupResource {
				foundExactMatch = true
				ret = append(ret, currKind)
			}
		}

		// if you didn't find an exact match, match on group prefixing. This allows storageclass.storage to match
		// storageclass.storage.k8s.io
		if !foundExactMatch {
			for currResource, currKind := range m.resourceToKind {
				if !strings.HasPrefix(currResource.Group, requestedGroupResource.Group) {
					continue
				}
				if currResource.Resource == requestedGroupResource.Resource {
					ret = append(ret, currKind)
				}
			}

		}

	case hasVersion:
		for currResource, currKind := range m.resourceToKind {
			if currResource.Version == resource.Version && currResource.Resource == resource.Resource {
				ret = append(ret, currKind)
			}
		}

	default:
		for currResource, currKind := range m.resourceToKind {
			if currResource.Resource == resource.Resource {
				ret = append(ret, currKind)
			}
		}
	}

	if len(ret) == 0 {
		return nil, &meta.NoResourceMatchError{PartialResource: input}
	}

	sort.Sort(kindByPreferredGroupVersion{ret, m.defaultGroupVersions})
	return ret, nil
}

// ResourceFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches
func (m *DynamicRESTMapper) ResourceFor(resource schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	resources, err := m.ResourcesFor(resource)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	if len(resources) == 1 {
		return resources[0], nil
	}

	return schema.GroupVersionResource{}, &meta.AmbiguousResourceError{PartialResource: resource, MatchingResources: resources}
}

func coerceResourceForMatching(resource schema.GroupVersionResource) schema.GroupVersionResource {
	resource.Resource = strings.ToLower(resource.Resource)
	if resource.Version == runtime.APIVersionInternal {
		resource.Version = ""
	}

	return resource
}

type resourceByPreferredGroupVersion struct {
	list      []schema.GroupVersionResource
	sortOrder []schema.GroupVersion
}

func (o resourceByPreferredGroupVersion) Len() int      { return len(o.list) }
func (o resourceByPreferredGroupVersion) Swap(i, j int) { o.list[i], o.list[j] = o.list[j], o.list[i] }
func (o resourceByPreferredGroupVersion) Less(i, j int) bool {
	lhs := o.list[i]
	rhs := o.list[j]
	if lhs == rhs {
		return false
	}

	if lhs.GroupVersion() == rhs.GroupVersion() {
		return lhs.Resource < rhs.Resource
	}

	// otherwise, the difference is in the GroupVersion, so we need to sort with respect to the preferred order
	lhsIndex := -1
	rhsIndex := -1

	for i := range o.sortOrder {
		if o.sortOrder[i] == lhs.GroupVersion() {
			lhsIndex = i
		}
		if o.sortOrder[i] == rhs.GroupVersion() {
			rhsIndex = i
		}
	}

	if rhsIndex == -1 {
		return true
	}

	return lhsIndex < rhsIndex
}

// ResourcesFor takes a partial resource and returns the list of potential resource in priority order
func (m *DynamicRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	resource := coerceResourceForMatching(input)

	hasResource := len(resource.Resource) > 0
	hasGroup := len(resource.Group) > 0
	hasVersion := len(resource.Version) > 0

	if !hasResource {
		return nil, fmt.Errorf("a resource must be present, got: %v", resource)
	}

	ret := []schema.GroupVersionResource{}
	switch {
	case hasGroup && hasVersion:
		// fully qualified.  Find the exact match
		for plural, singular := range m.pluralToSingular {
			if singular == resource {
				ret = append(ret, plural)
				break
			}
			if plural == resource {
				ret = append(ret, plural)
				break
			}
		}

	case hasGroup:
		// given a group, prefer an exact match.  If you don't find one, resort to a prefix match on group
		foundExactMatch := false
		requestedGroupResource := resource.GroupResource()
		for plural, singular := range m.pluralToSingular {
			if singular.GroupResource() == requestedGroupResource {
				foundExactMatch = true
				ret = append(ret, plural)
			}
			if plural.GroupResource() == requestedGroupResource {
				foundExactMatch = true
				ret = append(ret, plural)
			}
		}

		// if you didn't find an exact match, match on group prefixing. This allows storageclass.storage to match
		// storageclass.storage.k8s.io
		if !foundExactMatch {
			for plural, singular := range m.pluralToSingular {
				if !strings.HasPrefix(plural.Group, requestedGroupResource.Group) {
					continue
				}
				if singular.Resource == requestedGroupResource.Resource {
					ret = append(ret, plural)
				}
				if plural.Resource == requestedGroupResource.Resource {
					ret = append(ret, plural)
				}
			}

		}

	case hasVersion:
		for plural, singular := range m.pluralToSingular {
			if singular.Version == resource.Version && singular.Resource == resource.Resource {
				ret = append(ret, plural)
			}
			if plural.Version == resource.Version && plural.Resource == resource.Resource {
				ret = append(ret, plural)
			}
		}

	default:
		for plural, singular := range m.pluralToSingular {
			if singular.Resource == resource.Resource {
				ret = append(ret, plural)
			}
			if plural.Resource == resource.Resource {
				ret = append(ret, plural)
			}
		}
	}

	if len(ret) == 0 {
		return nil, &meta.NoResourceMatchError{PartialResource: resource}
	}

	sort.Sort(resourceByPreferredGroupVersion{ret, m.defaultGroupVersions})
	return ret, nil
}

// RESTMapping identifies a preferred resource mapping for the provided group kind.
func (m *DynamicRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	mappings, err := m.RESTMappings(gk, versions...)
	if err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}
	// since we rely on RESTMappings method
	// take the first match and return to the caller
	// as this was the existing behavior.
	return mappings[0], nil
}

// RESTMappings returns all resource mappings for the provided group kind if no
// version search is provided. Otherwise identifies a preferred resource mapping for
// the provided version(s).
func (m *DynamicRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	mappings := make([]*meta.RESTMapping, 0)
	potentialGVK := make([]schema.GroupVersionKind, 0)
	hadVersion := false

	// Pick an appropriate version
	for _, version := range versions {
		if len(version) == 0 || version == runtime.APIVersionInternal {
			continue
		}
		currGVK := gk.WithVersion(version)
		hadVersion = true
		if _, ok := m.kindToPluralResource[currGVK]; ok {
			potentialGVK = append(potentialGVK, currGVK)
			break
		}
	}
	// Use the default preferred versions
	if !hadVersion && len(potentialGVK) == 0 {
		for _, gv := range m.defaultGroupVersions {
			if gv.Group != gk.Group {
				continue
			}
			potentialGVK = append(potentialGVK, gk.WithVersion(gv.Version))
		}
	}

	if len(potentialGVK) == 0 {
		return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
	}

	for _, gvk := range potentialGVK {
		//Ensure we have a REST mapping
		res, ok := m.kindToPluralResource[gvk]
		if !ok {
			continue
		}

		// Ensure we have a REST scope
		scope, ok := m.kindToScope[gvk]
		if !ok {
			return nil, fmt.Errorf("the provided version %q and kind %q cannot be mapped to a supported scope", gvk.GroupVersion(), gvk.Kind)
		}

		mappings = append(mappings, &meta.RESTMapping{
			Resource:         res,
			GroupVersionKind: gvk,
			Scope:            scope,
		})
	}

	if len(mappings) == 0 {
		return nil, &meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{Group: gk.Group, Resource: gk.Kind}}
	}
	return mappings, nil
}

func (m *DynamicRESTMapper) ResourceSingularizer(resourceType string) (string, error) {
	partialResource := schema.GroupVersionResource{Resource: resourceType}
	resources, err := m.ResourcesFor(partialResource)
	if err != nil {
		return resourceType, err
	}

	singular := schema.GroupVersionResource{}
	for _, curr := range resources {
		currSingular, ok := m.pluralToSingular[curr]
		if !ok {
			continue
		}
		if singular.Empty() {
			singular = currSingular
			continue
		}

		if currSingular.Resource != singular.Resource {
			return resourceType, fmt.Errorf("multiple possible singular resources (%v) found for %v", resources, resourceType)
		}
	}

	if singular.Empty() {
		return resourceType, fmt.Errorf("no singular of resource %v has been defined", resourceType)
	}

	return singular.Resource, nil
}
