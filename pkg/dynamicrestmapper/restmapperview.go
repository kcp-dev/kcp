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

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
)

type ForCluster struct {
	clusterName logicalcluster.Name
	m           *DynamicRESTMapper
}

func newForCluster(clusterName logicalcluster.Name, m *DynamicRESTMapper) *ForCluster {
	return &ForCluster{
		clusterName: clusterName,
		m:           m,
	}
}

// KindFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches
func (v *ForCluster) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	kinds, err := v.KindsFor(resource)
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
func (v *ForCluster) KindsFor(input schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	resource := coerceResourceForMatching(input)

	hasResource := len(resource.Resource) > 0
	hasGroup := len(resource.Group) > 0
	hasVersion := len(resource.Version) > 0

	if !hasResource {
		return nil, fmt.Errorf("a resource must be present, got: %v", resource)
	}

	v.m.lock.RLock()
	defer v.m.lock.RUnlock()

	ms, ok := v.m.clusterToMapping[v.clusterName]
	if !ok {
		return nil, &meta.NoResourceMatchError{PartialResource: input}
	}

	ret := []schema.GroupVersionKind{}
	switch {
	// fully qualified.  Find the exact match
	case hasGroup && hasVersion:
		kind, exists := ms.resourceToKind[resource]
		if exists {
			ret = append(ret, kind)
		}

	case hasGroup:
		foundExactMatch := false
		requestedGroupResource := resource.GroupResource()
		for currResource, currKind := range ms.resourceToKind {
			if currResource.GroupResource() == requestedGroupResource {
				foundExactMatch = true
				ret = append(ret, currKind)
			}
		}

		// if you didn't find an exact match, match on group prefixing. This allows storageclass.storage to match
		// storageclass.storage.k8s.io
		if !foundExactMatch {
			for currResource, currKind := range ms.resourceToKind {
				if !strings.HasPrefix(currResource.Group, requestedGroupResource.Group) {
					continue
				}
				if currResource.Resource == requestedGroupResource.Resource {
					ret = append(ret, currKind)
				}
			}

		}

	case hasVersion:
		for currResource, currKind := range ms.resourceToKind {
			if currResource.Version == resource.Version && currResource.Resource == resource.Resource {
				ret = append(ret, currKind)
			}
		}

	default:
		for currResource, currKind := range ms.resourceToKind {
			if currResource.Resource == resource.Resource {
				ret = append(ret, currKind)
			}
		}
	}

	if len(ret) == 0 {
		return nil, &meta.NoResourceMatchError{PartialResource: input}
	}

	sort.Sort(kindByPreferredGroupVersion{ret, ms.defaultGroupVersions})
	return ret, nil
}

// ResourceFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches
func (v *ForCluster) ResourceFor(resource schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	resources, err := v.ResourcesFor(resource)
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
func (v *ForCluster) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	resource := coerceResourceForMatching(input)

	hasResource := len(resource.Resource) > 0
	hasGroup := len(resource.Group) > 0
	hasVersion := len(resource.Version) > 0

	if !hasResource {
		return nil, fmt.Errorf("a resource must be present, got: %v", resource)
	}

	v.m.lock.RLock()
	defer v.m.lock.RUnlock()

	ms, ok := v.m.clusterToMapping[v.clusterName]
	if !ok {
		return nil, &meta.NoResourceMatchError{PartialResource: input}
	}

	ret := []schema.GroupVersionResource{}
	switch {
	case hasGroup && hasVersion:
		// fully qualified.  Find the exact match
		for plural, singular := range ms.pluralToSingular {
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
		for plural, singular := range ms.pluralToSingular {
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
			for plural, singular := range ms.pluralToSingular {
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
		for plural, singular := range ms.pluralToSingular {
			if singular.Version == resource.Version && singular.Resource == resource.Resource {
				ret = append(ret, plural)
			}
			if plural.Version == resource.Version && plural.Resource == resource.Resource {
				ret = append(ret, plural)
			}
		}

	default:
		for plural, singular := range ms.pluralToSingular {
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

	sort.Sort(resourceByPreferredGroupVersion{ret, ms.defaultGroupVersions})
	return ret, nil
}

// RESTMapping identifies a preferred resource mapping for the provided group kind.
func (v *ForCluster) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	mappings, err := v.RESTMappings(gk, versions...)
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
func (v *ForCluster) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	mappings := make([]*meta.RESTMapping, 0)
	potentialGVK := make([]schema.GroupVersionKind, 0)
	hadVersion := false

	v.m.lock.RLock()
	defer v.m.lock.RUnlock()

	ms, ok := v.m.clusterToMapping[v.clusterName]
	if !ok {
		return nil, &meta.NoResourceMatchError{PartialResource: schema.GroupVersionResource{Group: gk.Group, Resource: gk.Kind}}
	}

	// Pick an appropriate version
	for _, version := range versions {
		if len(version) == 0 || version == runtime.APIVersionInternal {
			continue
		}
		currGVK := gk.WithVersion(version)
		hadVersion = true
		if _, ok := ms.kindToPluralResource[currGVK]; ok {
			potentialGVK = append(potentialGVK, currGVK)
			break
		}
	}
	// Use the default preferred versions
	if !hadVersion && len(potentialGVK) == 0 {
		for _, gv := range ms.defaultGroupVersions {
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
		res, ok := ms.kindToPluralResource[gvk]
		if !ok {
			continue
		}

		// Ensure we have a REST scope
		scope, ok := ms.kindToScope[gvk]
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

func (v *ForCluster) ResourceSingularizer(resourceType string) (string, error) {
	partialResource := schema.GroupVersionResource{Resource: resourceType}
	resources, err := v.ResourcesFor(partialResource)
	if err != nil {
		return resourceType, err
	}

	v.m.lock.RLock()
	defer v.m.lock.RUnlock()

	ms, ok := v.m.clusterToMapping[v.clusterName]
	if !ok {
		return resourceType, fmt.Errorf("no singular of resource %v has been defined", resourceType)
	}

	singular := schema.GroupVersionResource{}
	for _, curr := range resources {
		currSingular, ok := ms.pluralToSingular[curr]
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
