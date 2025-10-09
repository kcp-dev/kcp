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
)

// DynamicRESTMapper is a thread-safe RESTMapper with per-cluster GVK/GVR mappings.
// The mapping data is fed from its associated Controller (in this package), triggered
// on CRDs and APIBindings (and their respective APIResourceSchemas).
type DynamicRESTMapper struct {
	lock sync.RWMutex
	// Built-in types consist of core k8s types and system CRDs.
	// They are present in all clusters.
	builtin *DefaultRESTMapper
	// Dynamic types consist of bound types and CRDs local to the cluster.
	dynamic map[logicalcluster.Name]*DefaultRESTMapper
}

func NewDynamicRESTMapper() *DynamicRESTMapper {
	return &DynamicRESTMapper{
		builtin: NewDefaultRESTMapper(nil),
		dynamic: make(map[logicalcluster.Name]*DefaultRESTMapper),
	}
}

func (d *DynamicRESTMapper) deleteMappingsForCluster(clusterName logicalcluster.Name) {
	d.lock.Lock()
	defer d.lock.Unlock()
	delete(d.dynamic, clusterName)
}

// ForCluster returns a RESTMapper for the specified cluster name.
// The method never returns nil. If the cluster doesn't exist at the time
// of calling ForCluster, or if it is deleted while holding the returned
// RESTMapper instance, all RESTMapper's methods will return empty matches
// and a NoResourceMatchError error.
func (d *DynamicRESTMapper) ForCluster(clusterName logicalcluster.Name) *ForCluster {
	return newForCluster(clusterName, d)
}
