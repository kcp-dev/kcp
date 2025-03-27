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
	"k8s.io/apimachinery/pkg/api/meta"
)

// This file adds mutable methods to our fork of upstream's DefaultRESTMapper.

func (m *DefaultRESTMapper) empty() bool {
	// If one of the maps is empty, all of the maps are empty.
	return len(m.resourceToKind) == 0
}

func (m *DefaultRESTMapper) add(typeMeta typeMeta) {
	kind := typeMeta.groupVersionKind()
	singular := typeMeta.groupVersionResourceSingular()
	plural := typeMeta.groupVersionResourcePlural()

	m.singularToPlural[singular] = plural
	m.pluralToSingular[plural] = singular

	m.resourceToKind[singular] = kind
	m.resourceToKind[plural] = kind

	m.kindToPluralResource[kind] = plural
	m.kindToScope[kind] = meta.RESTScopeRoot
}

func (m *DefaultRESTMapper) remove(typeMeta typeMeta) {
	kind := typeMeta.groupVersionKind()
	singular := typeMeta.groupVersionResourceSingular()
	plural := typeMeta.groupVersionResourcePlural()

	delete(m.singularToPlural, singular)
	delete(m.pluralToSingular, plural)

	delete(m.resourceToKind, singular)
	delete(m.resourceToKind, plural)

	delete(m.kindToPluralResource, kind)
	delete(m.kindToScope, kind)
}

func (m *DefaultRESTMapper) apply(toRemove []typeMeta, toAdd []typeMeta) {
	visited := map[typeMeta]struct{}{}

	for i := range toAdd {
		m.add(toAdd[i])
		visited[toAdd[i]] = struct{}{}
	}

	for i := range toRemove {
		if _, hasVisited := visited[toRemove[i]]; !hasVisited {
			// Remove only mappings that haven't been added/updated.
			m.remove(toRemove[i])
		}
	}
}
