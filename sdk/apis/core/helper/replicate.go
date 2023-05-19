/*
Copyright 2023 The KCP Authors.

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

package helper

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/sdk/apis/core"
)

// ReplicateForValue returns the replicate value for the given controller added.
func ReplicateForValue(replicateValue, controller string) (result string, changed bool) {
	if replicateValue == "" {
		return controller, true
	}
	existing := sets.New[string](strings.Split(replicateValue, ",")...)
	if !existing.Has(controller) {
		existing.Insert(controller)
		return strings.Join(sets.List[string](existing), ","), true
	}
	return replicateValue, false
}

// DontReplicateForValue returns the replicate value for the given controller removed.
func DontReplicateForValue(replicateValue, controller string) (result string, changed bool) {
	if replicateValue == controller || replicateValue == "" {
		return "", replicateValue == controller
	}
	existing := sets.New[string](strings.Split(replicateValue, ",")...)
	if existing.Has(controller) {
		existing.Delete(controller)
		return strings.Join(sets.List[string](existing), ","), true
	}
	return replicateValue, false
}

// ReplicateFor ensures the controller string is part of the separated list of controller names
// in the internal.kcp.io/replicate label. This function changes the annotations in-place.
func ReplicateFor(annotations map[string]string, controller string) (result map[string]string, changed bool) {
	for k, v := range annotations {
		if k != core.ReplicateAnnotationKey {
			continue
		}

		existing := sets.New[string](strings.Split(v, ",")...)
		if !existing.Has(controller) {
			existing.Insert(controller)
			annotations[k] = strings.Join(sets.List[string](existing), ",")
			return annotations, true
		}
		return annotations, false
	}

	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[core.ReplicateAnnotationKey] = controller
	return annotations, true
}

// DontReplicateFor ensures the controller string is not part of the separated list of controller names
// in the internal.kcp.io/replicate label. This function changes the annotations in-place.
func DontReplicateFor(annotations map[string]string, controller string) (result map[string]string, changed bool) {
	for k, v := range annotations {
		if k != core.ReplicateAnnotationKey {
			continue
		}

		if v == controller {
			delete(annotations, k)
			return annotations, true
		}
		existing := sets.New[string](strings.Split(v, ",")...)
		if existing.Has(controller) {
			existing.Delete(controller)
			annotations[k] = strings.Join(sets.List[string](existing), ",")
			return annotations, true
		}
		return annotations, false
	}

	return annotations, false
}
