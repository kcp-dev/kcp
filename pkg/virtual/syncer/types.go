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

package syncer

import (
	"github.com/kcp-dev/logicalcluster"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
)

// WorkloadClusterRef is a reference to a WorkloadCluster resource that lives
// inside a given KCP logical cluster.
type WorkloadClusterRef struct {
	LogicalClusterName logicalcluster.Name
	Name               string
}

// Key build a key that will be used as the API domain key to retrieve all the APIs
// that should be exposed to a syncer living in a given workload cluster.
func (c WorkloadClusterRef) Key() string {
	return c.LogicalClusterName.String() + "/" + c.Name
}

// WorkloadClusterAPI defines an API that should be exposed for a given WorkloadCluster
type WorkloadClusterAPI struct {
	WorkloadClusterRef
	Spec *apiresourcev1alpha1.CommonAPIResourceSpec
}

// WorkloadClusterAPIManager provides the ablity to manage (add, modify, remove)
// APIs exposed on a given WorkloadCluster
type WorkloadClusterAPIManager interface {
	Upsert(api WorkloadClusterAPI) error
	Remove(api WorkloadClusterAPI) error
}
