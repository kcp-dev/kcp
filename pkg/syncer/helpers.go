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
	"strings"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// DeprecatedGetAssignedWorkloadCluster returns one assigned workload cluster in Sync state. It will
// likely lead to broken behaviour when there is one of those labels on a resource.
//
// Deprecated: use GetResourceState per cluster instead.
func DeprecatedGetAssignedWorkloadCluster(labels map[string]string) string {
	for k, v := range labels {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterResourceStateLabelPrefix) && v == string(workloadv1alpha1.ResourceStateSync) {
			return strings.TrimPrefix(k, workloadv1alpha1.InternalClusterResourceStateLabelPrefix)
		}
	}
	return ""
}
