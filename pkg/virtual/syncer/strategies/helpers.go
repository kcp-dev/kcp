/*
Copyright 2021 The KCP Authors.

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

package strategies

import (
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	SyncingTransformationAnnotationPrefix = "transformation.workloads.kcp.dev/"
)

func GetSyncing(kcpResource metav1.Object) Syncing {
	labels := kcpResource.GetLabels()
	syncing := make(Syncing, len(labels))
	annotations := kcpResource.GetAnnotations()
	for labelName, labelValue := range labels {
		if strings.HasPrefix(labelName, v1alpha1.InternalClusterResourceStateLabelPrefix) {
			syncTarget := strings.TrimPrefix(labelName, v1alpha1.InternalClusterResourceStateLabelPrefix)
			syncTargetSyncing := SyncTargetSyncing{
				RequestedState: RequestedSyncingState(labelValue),
				Detail:         annotations[SyncingTransformationAnnotationPrefix+syncTarget],
			}
			if deletionAnnotation, exists := annotations[v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTarget]; exists {
				var deletionTimestamp metav1.Time
				if err := deletionTimestamp.UnmarshalText([]byte(deletionAnnotation)); err != nil {
					klog.Errorf("Parsing of the deletion annotation for sync target %q failed: %v", syncTarget, err)
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
	return syncing
}

func CarryOnSyncerViewStatus(existingSyncerViewResource, newSyncerViewResource *unstructured.Unstructured) error {
	if existingSyncerViewResource != nil {
		// Set the status to the syncer view (last set status for this sync target)
		if status, exists, err := unstructured.NestedFieldCopy(existingSyncerViewResource.UnstructuredContent(), "status"); err != nil {
			return err
		} else if exists {
			if err := unstructured.SetNestedField(newSyncerViewResource.UnstructuredContent(), status, "status"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(newSyncerViewResource.UnstructuredContent(), "status")
		}
	}
	return nil
}

func GetSyncerViews(kcpResource *unstructured.Unstructured, syncTargets ...string) (map[string]unstructured.Unstructured, error) {
	kcpResource = kcpResource.DeepCopy()

	annotations := kcpResource.GetAnnotations()

	filterSyncTargets := len(syncTargets) > 0
	syncTargetsToKeep := sets.NewString(syncTargets...)

	syncerViewDiffs := make(map[string]string)
	for name, value := range annotations {
		if strings.HasPrefix(name, syncerViewAnnotationPrefix) {
			syncTarget := strings.TrimPrefix(name, syncerViewAnnotationPrefix)
			if filterSyncTargets && !syncTargetsToKeep.Has(syncTarget) {
				continue
			}
			syncerViewDiffs[syncTarget] = value
			delete(annotations, name)
		}
	}
	kcpResource.SetAnnotations(annotations)
	kcpResourceJson, err := kcpResource.MarshalJSON()
	if err != nil {
		return nil, err
	}

	syncerViewResources := make(map[string]unstructured.Unstructured)
	for syncTarget, syncerViewDiff := range syncerViewDiffs {
		syncerViewResourceJson, err := jsonpatch.MergePatch([]byte(kcpResourceJson), []byte(syncerViewDiff))
		if err != nil {
			return nil, err
		}

		var syncerViewResource unstructured.Unstructured
		if err := syncerViewResource.UnmarshalJSON([]byte(syncerViewResourceJson)); err != nil {
			return nil, err
		}
		syncerViewResources[syncTarget] = syncerViewResource
	}

	return syncerViewResources, nil
}
