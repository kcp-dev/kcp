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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var identityStrategy SyncStrategy = SyncStrategy{
	ReadFromKCP: func(workloadClusterName string, newKCPResource, existingSyncerViewResource *unstructured.Unstructured, requestedSyncing Syncing) (newSyncerViewResource *unstructured.Unstructured, err error) {
		newSyncerViewResource = newKCPResource.DeepCopy()

		if err := CarryOnSyncerViewStatus(existingSyncerViewResource, newSyncerViewResource); err != nil {
			return nil, err
		}

		return newSyncerViewResource, nil
	},
	UpdateFromSyncTarget: func(workloadClusterName string, newSyncerViewResource *unstructured.Unstructured, existingKCPResource *unstructured.Unstructured, existingSyncerViewResources map[string]unstructured.Unstructured, requestedPlacement Syncing) (newKCPResource *unstructured.Unstructured, err error) {
		return existingKCPResource.DeepCopy(), nil
	},
}

func IdentityStrategy() SyncStrategy {
	return identityStrategy
}
