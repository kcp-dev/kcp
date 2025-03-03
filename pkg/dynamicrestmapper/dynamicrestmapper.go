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
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type mapping struct {
	defaultGroupVersions []schema.GroupVersion

	resourceToKind       map[schema.GroupVersionResource]schema.GroupVersionKind
	kindToPluralResource map[schema.GroupVersionKind]schema.GroupVersionResource
	kindToScope          map[schema.GroupVersionKind]meta.RESTScope
	singularToPlural     map[schema.GroupVersionResource]schema.GroupVersionResource
	pluralToSingular     map[schema.GroupVersionResource]schema.GroupVersionResource
}

func newMapping() *mapping {
	return &mapping{
		resourceToKind:       make(map[schema.GroupVersionResource]schema.GroupVersionKind),
		kindToPluralResource: make(map[schema.GroupVersionKind]schema.GroupVersionResource),
		kindToScope:          make(map[schema.GroupVersionKind]meta.RESTScope),
		singularToPlural:     make(map[schema.GroupVersionResource]schema.GroupVersionResource),
		pluralToSingular:     make(map[schema.GroupVersionResource]schema.GroupVersionResource),
	}
}

func (m *mapping) add(kind schema.GroupVersionKind, singular, plural schema.GroupVersionResource, scope meta.RESTScope) {
	m.singularToPlural[singular] = plural
	m.pluralToSingular[plural] = singular

	m.resourceToKind[singular] = kind
	m.resourceToKind[plural] = kind

	m.kindToPluralResource[kind] = plural
	m.kindToScope[kind] = scope
}

func (m *mapping) remove(kind schema.GroupVersionKind) {
	singular := m.kindToPluralResource[kind]
	plural := m.singularToPlural[singular]

	delete(m.kindToPluralResource, kind)
	delete(m.kindToScope, kind)
	delete(m.singularToPlural, singular)
	delete(m.pluralToSingular, plural)
	delete(m.resourceToKind, plural)
}

func (m *mapping) empty() bool {
	return len(m.resourceToKind) == 0 &&
		len(m.kindToPluralResource) == 0 &&
		len(m.kindToScope) == 0 &&
		len(m.singularToPlural) == 0 &&
		len(m.pluralToSingular) == 0
}

type DynamicRESTMapper struct {
	lock             sync.RWMutex
	clusterToMapping map[logicalcluster.Name]*mapping
}

func NewDynamicRESTMapper(defaultGroupVersions []schema.GroupVersion) *DynamicRESTMapper {
	return &DynamicRESTMapper{
		clusterToMapping: make(map[logicalcluster.Name]*mapping),
	}
}

// Creates and inserts a new instance of mapping into DynamicRESTMapper's clusterToMapping map.
// The caller is responsible for locking the DynamicRESTMapper.lock mutex.
func (d *DynamicRESTMapper) getOrCreateMappingForCluster(clusterName logicalcluster.Name) *mapping {
	mappingForCluster, mappingFound := d.clusterToMapping[clusterName]
	if !mappingFound {
		mappingForCluster = newMapping()
		d.clusterToMapping[clusterName] = mappingForCluster
	}

	return mappingForCluster
}

func (d *DynamicRESTMapper) add(clusterName logicalcluster.Name, typeMeta gvkr, scope meta.RESTScope) {
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

	d.lock.Lock()
	defer d.lock.Unlock()

	d.getOrCreateMappingForCluster(clusterName).add(kind, singular, plural, scope)
}

func (d *DynamicRESTMapper) remove(clusterName logicalcluster.Name, gvk schema.GroupVersionKind) {
	d.lock.Lock()
	defer d.lock.Unlock()

	mappingForCluster, mappingFound := d.clusterToMapping[clusterName]
	if !mappingFound {
		return
	}

	mappingForCluster.remove(gvk)
	if mappingForCluster.empty() {
		delete(d.clusterToMapping, clusterName)
	}
}

func (d *DynamicRESTMapper) For(clusterName logicalcluster.Name) (meta.RESTMapper, error) {
	d.lock.Lock()
	defer d.lock.Unlock()

	mappingForCluster := d.getOrCreateMappingForCluster(clusterName)

	return newRESTMapperView(&d.lock, mappingForCluster), nil
}
