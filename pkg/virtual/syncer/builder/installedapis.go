/*
Copyright 2022 The KCP Authors.

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

package builder

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	apidefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

var _ syncer.WorkloadClusterAPIManager = (*installedAPIs)(nil)

type installedAPIs struct {
	createAPIDefinition apidefs.CreateAPIDefinitionFunc

	mutex   sync.RWMutex
	apiSets map[string]apidefs.APISet
}

func newInstalledAPIs(createAPIDefinition apidefs.CreateAPIDefinitionFunc) *installedAPIs {
	return &installedAPIs{
		createAPIDefinition: createAPIDefinition,
		apiSets:             make(map[string]apidefs.APISet),
	}
}

func (apis *installedAPIs) addWorkloadCluster(workloadCluster syncer.WorkloadClusterRef) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	workloadClusterKey := workloadCluster.Key()
	if _, exists := apis.apiSets[workloadClusterKey]; !exists {
		apis.apiSets[workloadClusterKey] = make(apidefs.APISet)
	}
}

func (apis *installedAPIs) removeWorkloadCluster(workloadCluster syncer.WorkloadClusterRef) {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	workloadClusterKey := workloadCluster.Key()
	delete(apis.apiSets, workloadClusterKey)
}

func (apis *installedAPIs) GetAPIs(apiDomainKey string) (apidefs.APISet, bool) {
	apis.mutex.RLock()
	defer apis.mutex.RUnlock()

	apiSet, ok := apis.apiSets[apiDomainKey]
	return apiSet, ok
}

func (apis *installedAPIs) Upsert(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if workloadClusterAPIs, exists := apis.apiSets[api.Key()]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.Name, api.LogicalClusterName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		if apiDefinition, err := apis.createAPIDefinition(api.LogicalClusterName, api.Spec); err != nil {
			return err
		} else {
			workloadClusterAPIs[gvr] = apiDefinition
		}
	}
	return nil
}

func (apis *installedAPIs) Remove(api syncer.WorkloadClusterAPI) error {
	apis.mutex.Lock()
	defer apis.mutex.Unlock()

	if workloadClusterAPIs, exists := apis.apiSets[api.Key()]; !exists {
		return fmt.Errorf("workload cluster %q in workspace %q is unknown", api.Name, api.LogicalClusterName)
	} else {
		gvr := schema.GroupVersionResource{
			Group:    api.Spec.GroupVersion.Group,
			Version:  api.Spec.GroupVersion.Version,
			Resource: api.Spec.Plural,
		}
		delete(workloadClusterAPIs, gvr)
	}
	return nil
}
