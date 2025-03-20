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

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
)

type DynamicRESTMapper struct {
	lock      sync.RWMutex
	byCluster map[logicalcluster.Name]*DefaultRESTMapper
}

func NewDynamicRESTMapper(defaultGroupVersions []schema.GroupVersion) *DynamicRESTMapper {
	return &DynamicRESTMapper{
		byCluster: make(map[logicalcluster.Name]*DefaultRESTMapper),
	}
}

func (d *DynamicRESTMapper) getOrCreateClusterMapping(clusterName logicalcluster.Name) *DefaultRESTMapper {
	m := d.byCluster[clusterName]
	if m == nil {
		m = NewDefaultRESTMapper(nil)
		d.byCluster[clusterName] = m
	}

	return m
}

func (d *DynamicRESTMapper) applyForCluster(clusterName logicalcluster.Name, toRemove []gvkr, toAdd []gvkr) {
	d.lock.Lock()
	defer d.lock.Unlock()

	m := d.getOrCreateClusterMapping(clusterName)
	m.apply(toRemove, toAdd)

	if m.empty() {
		delete(d.byCluster, clusterName)
	}
}

func (d *DynamicRESTMapper) ForCluster(clusterName logicalcluster.Name) meta.RESTMapper {
	return newForCluster(clusterName, d)
}
