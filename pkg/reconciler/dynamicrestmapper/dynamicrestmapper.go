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

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
)

// DynamicRESTMapper is a thread-safe RESTMapper with per-cluster GVK/GVR mappings.
// The mapping data is fed from its associated Controller (in this package), triggered
// on CRDs and APIBindings (and their respective APIResourceSchemas). Additionally,
// mappings for built-in types (pkg/virtual/apiexport/schemas/builtin/builtin.go) are
// added for each LogicalCluster by default.
type DynamicRESTMapper struct {
	lock      sync.RWMutex
	byCluster map[logicalcluster.Name]*DefaultRESTMapper
}

func NewDynamicRESTMapper(defaultGroupVersions []schema.GroupVersion) *DynamicRESTMapper {
	return &DynamicRESTMapper{
		byCluster: make(map[logicalcluster.Name]*DefaultRESTMapper),
	}
}

func (d *DynamicRESTMapper) deleteMappingsForCluster(clusterName logicalcluster.Name) {
	d.lock.Lock()
	defer d.lock.Unlock()
	delete(d.byCluster, clusterName)
}

// ForCluster returns a RESTMapper for the specified cluster name.
// The method never returns nil. If the cluster doesn't exist at the time
// of calling ForCluster, or if it is deleted while holding the returned
// RESTMapper instance, all RESTMapper's methods will empty matches and
// NoResourceMatchError error.
func (d *DynamicRESTMapper) ForCluster(clusterName logicalcluster.Name) *ForCluster {
	return newForCluster(clusterName, d)
}
