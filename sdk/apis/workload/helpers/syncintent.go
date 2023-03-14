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

package helpers

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// SyncIntent gathers all the information related to the Syncing of
// a resource to a given SynTarget. This information comes from labels and
// annotations.
type SyncIntent struct {
	// ResourceState is the requested syncing state for this SyncTarget.
	// It is read on the state.workload.kcp.io/<syncTargetKey> label
	ResourceState v1alpha1.ResourceState

	// DeletionTimestamp is the parsed timestamp coming from the content of
	// the deletion.internal.workload.kcp.io/<syncTargetKey> annotation.
	// It expresses the timestamped intent that a resource should be removed
	// the given SyncTarget
	DeletionTimestamp *metav1.Time

	// Finalizers is the list of "soft" finalizers defined for this resource
	// and this SyncTarget.
	// It is read on the finalizers.workload.kcp.io/<syncTargetKey> annotation.
	Finalizers string
}

// GetSyncIntents gathers, for each SyncTarget, all the information related
// to the Syncing of the resource to this SyncTarget.
// This information comes from labels and annotations.
// Keys in the returns map are SyncTarget keys.
func GetSyncIntents(upstreamResource metav1.Object) (map[string]SyncIntent, error) {
	labels := upstreamResource.GetLabels()
	syncing := make(map[string]SyncIntent, len(labels))
	annotations := upstreamResource.GetAnnotations()
	for labelName, labelValue := range labels {
		if strings.HasPrefix(labelName, v1alpha1.ClusterResourceStateLabelPrefix) {
			syncTarget := strings.TrimPrefix(labelName, v1alpha1.ClusterResourceStateLabelPrefix)
			syncTargetSyncing := SyncIntent{
				ResourceState: v1alpha1.ResourceState(labelValue),
			}
			if deletionAnnotation, exists := annotations[v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTarget]; exists {
				var deletionTimestamp metav1.Time
				if err := deletionTimestamp.UnmarshalText([]byte(deletionAnnotation)); err != nil {
					return nil, fmt.Errorf("parsing of the deletion annotation for sync target %q failed: %w", syncTarget, err)
				} else {
					syncTargetSyncing.DeletionTimestamp = &deletionTimestamp
				}
			}
			if finalizersAnnotation, exists := annotations[v1alpha1.ClusterFinalizerAnnotationPrefix+syncTarget]; exists {
				syncTargetSyncing.Finalizers = finalizersAnnotation
			}
			syncing[syncTarget] = syncTargetSyncing
		}
	}
	return syncing, nil
}
