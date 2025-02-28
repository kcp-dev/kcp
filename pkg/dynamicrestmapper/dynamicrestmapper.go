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
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
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
	return schema.GroupVersionKind{}, nil
}

// KindsFor takes a partial resource and returns the list of potential kinds in priority order
func (m *DynamicRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}

// ResourceFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches
func (m *DynamicRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}

// ResourcesFor takes a partial resource and returns the list of potential resource in priority order
func (m *DynamicRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}

// RESTMapping identifies a preferred resource mapping for the provided group kind.
func (m *DynamicRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	return nil, nil
}

// RESTMappings returns all resource mappings for the provided group kind if no
// version search is provided. Otherwise identifies a preferred resource mapping for
// the provided version(s).
func (m *DynamicRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return nil, nil
}

func (m *DynamicRESTMapper) ResourceSingularizer(resource string) (singular string, err error) {
	return "", nil
}
