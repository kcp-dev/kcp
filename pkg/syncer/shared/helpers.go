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

package shared

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kube-openapi/pkg/util/sets"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// SyncableClusterScopedResources holds a set of cluster-wide GVR that are allowed to be synced.
var SyncableClusterScopedResources = sets.NewString(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}.String())

// DeprecatedGetAssignedSyncTarget returns one assigned sync target in Sync state. It will
// likely lead to broken behaviour when there is one of those labels on a resource.
//
// Deprecated: use GetResourceState per cluster instead.
func DeprecatedGetAssignedSyncTarget(labels map[string]string) string {
	for k, v := range labels {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) && v == string(workloadv1alpha1.ResourceStateSync) {
			return strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix)
		}
	}
	return ""
}

// GetUpstreamResourceName returns the name with which the resource is known upstream.
func GetUpstreamResourceName(downstreamResourceGVR schema.GroupVersionResource, downstreamResourceName string) string {
	configMapGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

	if downstreamResourceGVR == configMapGVR && downstreamResourceName == "kcp-root-ca.crt" {
		return "kube-root-ca.crt"
	}
	if downstreamResourceGVR == secretGVR && strings.HasPrefix(downstreamResourceName, "kcp-default-token") {
		return strings.TrimPrefix(downstreamResourceName, "kcp-")
	}
	return downstreamResourceName
}

// GetDNSID returns a unique ID for DNS object derived from the sync target name, its UID and workspace. It's
// a valid DNS segment and can be used as namespace or object names.
func GetDNSID(workspace logicalcluster.Path, syncTargetUID types.UID, syncTargetName string) string {
	syncerHash := sha256.Sum224([]byte(syncTargetUID))
	uid36hash := strings.ToLower(base36.EncodeBytes(syncerHash[:]))
	workspaceHash := sha256.Sum224([]byte(workspace.String()))
	workspace36hash := strings.ToLower(base36.EncodeBytes(workspaceHash[:]))

	return fmt.Sprintf("kcp-dns-%s-%s-%s", syncTargetName, uid36hash[:8], workspace36hash[:8])
}
