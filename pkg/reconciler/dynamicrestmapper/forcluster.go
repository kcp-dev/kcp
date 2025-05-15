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
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Thread-safe wrapper around LogicalCluster->DefaultRESTMapper mapping.
type ForCluster struct {
	clusterName logicalcluster.Name
	parent      *DynamicRESTMapper
}

func (v *ForCluster) clusterMappingOrEmpty(clusterName logicalcluster.Name) *DefaultRESTMapper {
	if m := v.parent.byCluster[clusterName]; m != nil {
		return m
	}
	return NewDefaultRESTMapper(nil)
}

func newForCluster(clusterName logicalcluster.Name, parent *DynamicRESTMapper) *ForCluster {
	return &ForCluster{
		clusterName: clusterName,
		parent:      parent,
	}
}

// KindFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches.
func (v *ForCluster) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).KindFor(resource)
}

// KindsFor takes a partial resource and returns the list of potential kinds in priority order.
func (v *ForCluster) KindsFor(input schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).KindsFor(input)
}

// ResourceFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches.
func (v *ForCluster) ResourceFor(resource schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).ResourceFor(resource)
}

// ResourcesFor takes a partial resource and returns the list of potential resource in priority order.
func (v *ForCluster) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).ResourcesFor(input)
}

// RESTMapping identifies a preferred resource mapping for the provided group kind.
func (v *ForCluster) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).RESTMapping(gk)
}

// RESTMappings returns all resource mappings for the provided group kind if no
// version search is provided. Otherwise identifies a preferred resource mapping for
// the provided version(s).
func (v *ForCluster) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).RESTMappings(gk, versions...)
}

func (v *ForCluster) ResourceSingularizer(resourceType string) (string, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()
	return v.clusterMappingOrEmpty(v.clusterName).ResourceSingularizer(resourceType)
}

func (v *ForCluster) apply(toRemove []typeMeta, toAdd []typeMeta) {
	v.parent.lock.Lock()
	defer v.parent.lock.Unlock()

	m := v.parent.byCluster[v.clusterName]
	if m == nil {
		m = NewDefaultRESTMapper(nil)
		v.parent.byCluster[v.clusterName] = m
	}

	m.apply(toRemove, toAdd)

	if m.empty() {
		delete(v.parent.byCluster, v.clusterName)
	}
}

func (v *ForCluster) getGVKRs(gr schema.GroupResource) ([]typeMeta, error) {
	v.parent.lock.Lock()
	defer v.parent.lock.Unlock()
	return v.clusterMappingOrEmpty(v.clusterName).getGVKRs(gr)
}
