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
	"encoding/json"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

// getSyncerViewFields builds a map whose keys are the summarizing field paths,
// and values are the overriding values of the corresponding fields for this SyncTarget.
// This map is built from the value of the diff.syncer.internal.kcp.io/<syncTargetKey> annotation.
func getSyncerViewFields(upstreamResource *unstructured.Unstructured, syncTargetKey string) (map[string]interface{}, error) {
	annotations := upstreamResource.GetAnnotations()
	var syncerViewAnnotationValue string
	var syncerViewAnnotationFound bool
	for name, value := range annotations {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			if syncTargetKey == strings.TrimPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
				syncerViewAnnotationValue = value
				syncerViewAnnotationFound = true
				break
			}
		}
	}

	if !syncerViewAnnotationFound {
		return nil, nil
	}

	result := make(map[string]interface{}, 1)
	if err := json.Unmarshal([]byte(syncerViewAnnotationValue), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// setSyncerViewFields unmarshalls into the diff.syncer.internal.kcp.io/<syncTargetKey> annotation
// a map whose keys are summarizing field keys, and values are the overridden values of the corresponding
// fields for this SyncTarget.
func setSyncerViewFields(kcpResource *unstructured.Unstructured, syncTargetKey string, syncerViewFieldValues map[string]interface{}) error {
	annotations := kcpResource.GetAnnotations()

	annotationValue, err := json.Marshal(syncerViewFieldValues)
	if err != nil {
		return err
	}

	if annotations == nil {
		annotations = make(map[string]string, 1)
	}

	annotations[v1alpha1.InternalSyncerViewAnnotationPrefix+syncTargetKey] = string(annotationValue)
	kcpResource.SetAnnotations(annotations)
	return nil
}
