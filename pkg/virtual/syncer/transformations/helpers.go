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

package transformations

import (
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func GetSyncing(kcpResource metav1.Object) map[string]SyncTargetSyncing {
	labels := kcpResource.GetLabels()
	syncing := make(map[string]SyncTargetSyncing, len(labels))
	annotations := kcpResource.GetAnnotations()
	for labelName, labelValue := range labels {
		if strings.HasPrefix(labelName, v1alpha1.ClusterResourceStateLabelPrefix) {
			syncTarget := strings.TrimPrefix(labelName, v1alpha1.ClusterResourceStateLabelPrefix)
			syncTargetSyncing := SyncTargetSyncing{
				ResourceState: v1alpha1.ResourceState(labelValue),
			}
			if deletionAnnotation, exists := annotations[v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTarget]; exists {
				var deletionTimestamp metav1.Time
				if err := deletionTimestamp.UnmarshalText([]byte(deletionAnnotation)); err != nil {
					klog.Warningf("Parsing of the deletion annotation for sync target %q failed: %v", syncTarget, err)
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

func getSyncerViews(kcpResource *unstructured.Unstructured, syncTargetFilter func(string) bool) (map[string]unstructured.Unstructured, error) {
	kcpResource = kcpResource.DeepCopy()

	annotations := kcpResource.GetAnnotations()

	syncerViewDiffs := make(map[string]string)
	for name, value := range annotations {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			syncTarget := strings.TrimPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix)
			if !syncTargetFilter(syncTarget) {
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

func GetAllSyncerViews(kcpResource *unstructured.Unstructured) (map[string]unstructured.Unstructured, error) {
	return getSyncerViews(kcpResource, func(string) bool {
		return true
	})
}

func GetSyncerView(kcpResource *unstructured.Unstructured, syncTargetName string) (*unstructured.Unstructured, error) {
	result, err := getSyncerViews(kcpResource, func(aSyncTarget string) bool {
		if aSyncTarget == syncTargetName {
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	syncerView := result[syncTargetName]
	return &syncerView, nil
}
