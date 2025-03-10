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
	"slices"
)

// This file adds mutable methods to our fork of upstream's DefaultRESTMapper.

func (m *DefaultRESTMapper) empty() bool {
	// If one of the maps is empty, all of the maps are empty.
	return len(m.resourceToKind) == 0
}

func (m *DefaultRESTMapper) add(typeMeta gvkr) {
	kind := typeMeta.groupVersionKind()
	singular := typeMeta.groupVersionResourceSingular()
	plural := typeMeta.groupVersionResourcePlural()

	m.singularToPlural[singular] = plural
	m.pluralToSingular[plural] = singular

	m.resourceToKind[singular] = kind
	m.resourceToKind[plural] = kind

	m.kindToPluralResource[kind] = plural
}

func (m *DefaultRESTMapper) remove(typeMeta gvkr) {
	kind := typeMeta.groupVersionKind()
	singular := m.kindToPluralResource[kind]
	plural := m.singularToPlural[typeMeta.groupVersionResourceSingular()]

	delete(m.kindToPluralResource, kind)
	delete(m.kindToScope, kind)
	delete(m.singularToPlural, singular)
	delete(m.pluralToSingular, plural)
	delete(m.resourceToKind, plural)
}

func (m *DefaultRESTMapper) apply(toRemove []gvkr, toAdd []gvkr) {
	if slices.Equal(toRemove, toAdd) {
		return
	}

	for i := range toRemove {
		m.remove(toRemove[i])
	}

	for i := range toAdd {
		m.add(toAdd[i])
	}
}
