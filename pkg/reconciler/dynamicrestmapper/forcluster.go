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
	"errors"

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
	if m := v.parent.dynamic[clusterName]; m != nil {
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

	if gvk, err := v.parent.builtin.KindFor(resource); err == nil {
		return gvk, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) {
		return schema.GroupVersionKind{}, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).KindFor(resource)
}

// KindsFor takes a partial resource and returns the list of potential kinds in priority order.
func (v *ForCluster) KindsFor(input schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if gvks, err := v.parent.builtin.KindsFor(input); err == nil {
		return gvks, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) {
		return nil, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).KindsFor(input)
}

// ResourceFor takes a partial resource and returns the single match.  Returns an error if there are multiple matches.
func (v *ForCluster) ResourceFor(resource schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if gvr, err := v.parent.builtin.ResourceFor(resource); err == nil {
		return gvr, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) {
		return schema.GroupVersionResource{}, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).ResourceFor(resource)
}

// ResourcesFor takes a partial resource and returns the list of potential resource in priority order.
func (v *ForCluster) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if gvrs, err := v.parent.builtin.ResourcesFor(input); err == nil {
		return gvrs, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) {
		return nil, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).ResourcesFor(input)
}

// RESTMapping identifies a preferred resource mapping for the provided group kind.
func (v *ForCluster) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if mapping, err := v.parent.builtin.RESTMapping(gk, versions...); err == nil {
		return mapping, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) && !errors.Is(err, &meta.NoKindMatchError{}) {
		return nil, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).RESTMapping(gk, versions...)
}

// RESTMappings returns all resource mappings for the provided group kind if no
// version search is provided. Otherwise identifies a preferred resource mapping for
// the provided version(s).
func (v *ForCluster) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if mappings, err := v.parent.builtin.RESTMappings(gk, versions...); err == nil {
		return mappings, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) && !errors.Is(err, &meta.NoKindMatchError{}) {
		return nil, err
	}

	return v.clusterMappingOrEmpty(v.clusterName).RESTMappings(gk, versions...)
}

func (v *ForCluster) ResourceSingularizer(resourceType string) (string, error) {
	v.parent.lock.RLock()
	defer v.parent.lock.RUnlock()

	if singular, err := v.parent.builtin.ResourceSingularizer(resourceType); err == nil {
		return singular, nil
	} else if !errors.Is(err, &meta.NoResourceMatchError{}) {
		return "", err
	}

	return v.clusterMappingOrEmpty(v.clusterName).ResourceSingularizer(resourceType)
}

func (v *ForCluster) apply(toRemove []typeMeta, toAdd []typeMeta) {
	v.parent.lock.Lock()
	defer v.parent.lock.Unlock()

	m := v.parent.dynamic[v.clusterName]
	if m == nil {
		m = NewDefaultRESTMapper(nil)
		v.parent.dynamic[v.clusterName] = m
	}

	m.apply(toRemove, toAdd)

	if m.empty() {
		delete(v.parent.dynamic, v.clusterName)
	}
}

func (v *ForCluster) getGVKRs(gr schema.GroupResource) ([]typeMeta, error) {
	v.parent.lock.Lock()
	defer v.parent.lock.Unlock()
	return v.clusterMappingOrEmpty(v.clusterName).getGVKRs(gr)
}
