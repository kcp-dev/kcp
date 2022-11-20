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

package projection

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

var projectedAPIs map[schema.GroupVersionResource]struct{}

func init() {
	projectedAPIs = map[schema.GroupVersionResource]struct{}{
		tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"): {},
	}
}

// Includes returns true if gvr is for a projected API. An API is projected if it is not stored in etcd and instead
// comes from some other data that is actually stored in etcd. For example, Workspaces (tenancy.kcp.dev/v1beta1) are
// projected; the real data comes from ClusterWorkspaces (tenancy.kcp.dev/v1alpha1).
func Includes(gvr schema.GroupVersionResource) bool {
	_, exists := projectedAPIs[gvr]
	return exists
}

// ProjectedAPIs returns the set of GVRs for projected APIs.
func ProjectedAPIs() map[schema.GroupVersionResource]struct{} {
	ret := make(map[schema.GroupVersionResource]struct{}, len(projectedAPIs))
	for gvr := range projectedAPIs {
		ret[gvr] = struct{}{}
	}
	return ret
}
